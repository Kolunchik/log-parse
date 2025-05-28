package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kolunchik/zs"
)

func line() string {
	return "2023-04-11T08:57:01.058999+03:00 10.77.2.11 %OLT: Interface EPON0/1:18's \"CTC\" OAM extension negotiated \n successfully!"
}

var opts struct {
	pn       string
	ln       string
	key      string
	zs       string
	zp       int
	buf      int
	batch    int
	interval time.Duration
}

func parseFlags() {
	flag.StringVar(&opts.pn, "posfile", "/var/tmp/log-parser.txt", "file to store position")
	flag.StringVar(&opts.ln, "logfile", "/var/log/net.log", "path to logfile")
	flag.StringVar(&opts.key, "zabbix-key", "net.log", "key for zabbix trapper")
	flag.StringVar(&opts.zs, "zabbix-server", "127.0.0.1", "zabbix server address")
	flag.IntVar(&opts.zp, "zabbix-port", 10051, "zabbix server port")
	flag.IntVar(&opts.buf, "buffer-size", 64*1024, "read buffer size in bytes")
	flag.IntVar(&opts.batch, "batch-size", 100, "max items per batch")
	flag.DurationVar(&opts.interval, "interval", time.Second*30, "interval between iterations")
	flag.Parse()
}

type parser struct {
	lock sync.Mutex
	file *os.File
	ln   string
	pos  *os.File
	pn   string
	hup  bool
}

func NewParser(ln, pn string) *parser {
	return &parser{
		ln: ln,
		pn: pn,
	}
}

func (p *parser) OpenLogFile() error {
	p.lock.Lock()
	defer p.lock.Unlock()
	var err error
	if p.file != nil {
		if err := processLogFile(p.file); err != nil {
			return fmt.Errorf("OpenLogFile(): %w", err)
		}
		if err := p.file.Close(); err != nil {
			return fmt.Errorf("OpenLogFile() close log file: %w", err)
		}
	}
	p.file, err = os.Open(p.ln)
	if err != nil {
		return fmt.Errorf("OpenLogFile() open log file: %w", err)
	}
	return nil
}

func (p *parser) RotateLogFile() error {
	rotated, err := p.IsRotated()
	if err != nil {
		return fmt.Errorf("RotateLogFile(): %w", err)
	}
	if !rotated {
		return nil
	}
	return p.OpenLogFile()
}

func (p *parser) OpenPositionFile() error {
	p.lock.Lock()
	defer p.lock.Unlock()
	var err error
	p.pos, err = os.OpenFile(p.pn, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return fmt.Errorf("OpenPositionFile() open position file: %w", err)
	}
	return nil
}

func (p *parser) SavePositionFile() error {
	p.lock.Lock()
	defer p.lock.Unlock()
	logStat, err := p.file.Stat()
	if err != nil {
		return fmt.Errorf("SavePositionFile() stat log file: %w", err)
	}
	position, err := p.file.Seek(0, io.SeekCurrent)
	if err != nil {
		return fmt.Errorf("SavePositionFile() seek log file: %w", err)
	}
	if err := p.pos.Truncate(0); err != nil {
		return fmt.Errorf("SavePositionFile() truncate position file: %w", err)
	}
	if _, err := p.pos.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("SavePositionFile() seek position file: %w", err)
	}
	if _, err := fmt.Fprintf(p.pos, "%v %v", position, logStat.ModTime().UnixNano()); err != nil {
		return fmt.Errorf("SavePositionFile() write position file: %w", err)
	}
	if err := p.pos.Sync(); err != nil {
		return fmt.Errorf("SavePositionFile() sync position file: %w", err)
	}
	log.Printf("New position saved: %d", position)
	return nil
}

func (p *parser) RestorePosition() error {
	p.lock.Lock()
	defer p.lock.Unlock()
	logStat, err := p.file.Stat()
	if err != nil {
		return fmt.Errorf("RestorePosition() stat log file: %w", err)
	}
	var position, ts int64
	if _, err := p.pos.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("RestorePosition() seek position file: %w", err)
	}
	if q, err := fmt.Fscanf(p.pos, "%v %v", &position, &ts); err != nil || position > logStat.Size() || q != 2 {
		position = 0
	}
	if _, err = p.file.Seek(position, io.SeekStart); err != nil {
		return fmt.Errorf("RestorePosition() seek log file: %w", err)
	}
	log.Printf("Position %v, logfile size %v, date %v", position, logStat.Size(), logStat.ModTime())
	return nil
}

func (p *parser) IsRotated() (bool, error) {
	p.lock.Lock()
	defer p.lock.Unlock()
	logStat, err := p.file.Stat()
	if err != nil {
		return false, fmt.Errorf("IsRotated() stat current log file: %w", err)
	}
	fileN, err := os.Open(p.ln)
	if err != nil {
		return false, fmt.Errorf("IsRotated() open new log file: %w", err)
	}
	defer fileN.Close()
	nStat, err := fileN.Stat()
	if err != nil {
		return false, fmt.Errorf("IsRotated() stat new log file: %w", err)
	}
	result := !os.SameFile(logStat, nStat)
	k := map[bool]string{true: "different files", false: "same file"}
	log.Printf("Current log file size: %v, new log file size: %v, %v", logStat.Size(), nStat.Size(), k[result])
	return result, nil
}

func (p *parser) Magic() error {
	p.lock.Lock()
	defer p.lock.Unlock()
	if err := processLogFile(p.file); err != nil {
		return fmt.Errorf("Magic(): %w", err)
	}
	return nil
}

func (p *parser) Init() error {
	if err := p.OpenLogFile(); err != nil {
		return fmt.Errorf("Init(): %w", err)
	}
	if err := p.OpenPositionFile(); err != nil {
		return fmt.Errorf("Init(): %w", err)
	}
	if err := p.RestorePosition(); err != nil {
		return fmt.Errorf("Init(): %w", err)
	}
	return nil
}

func (p *parser) Close() error {
	p.lock.Lock()
	defer p.lock.Unlock()
	if err := p.file.Close(); err != nil {
		return fmt.Errorf("Close() close log file: %w", err)
	}
	if err := p.pos.Close(); err != nil {
		return fmt.Errorf("Close() close position file: %w", err)
	}
	return nil
}

func sendData(data *[]zs.ZabbixDataItem) error {
	if len(*data) < 1 {
		return nil
	}
	sender := zs.NewSender(opts.zs, opts.zp)
	resp, err := sender.Send(*data)
	log.Println("Zabbix response:", resp)
	if err != nil {
		err = fmt.Errorf("sendData(): %w", err)
	}
	return err
}

func validateLogLine(parsed *[]string) bool {
	if len(*parsed) != 3 {
		return false
	}
	if _, err := time.Parse(time.RFC3339Nano, (*parsed)[0]); err != nil {
		return false
	}
	if _, err := netip.ParseAddr((*parsed)[1]); err != nil {
		return false
	}
	return true
}

func parseLogLine(line string) (zs.ZabbixDataItem, error) {
	parsed := strings.SplitN(line, " ", 3)
	if !validateLogLine(&parsed) {
		return zs.ZabbixDataItem{}, fmt.Errorf("parseLogLine(): invalid log format: %q", line)
	}
	return zs.ZabbixDataItem{
		Host:  parsed[1],
		Key:   opts.key,
		Value: parsed[0] + " " + parsed[2],
	}, nil
}

func processLogFile(logfile *os.File) error {
	scanner := bufio.NewScanner(logfile)
	buf := make([]byte, 0, opts.buf)
	scanner.Buffer(buf, cap(buf))

	data := make([]zs.ZabbixDataItem, 0)
	var linesProcessed int

	for scanner.Scan() {
		line := scanner.Text()
		item, err := parseLogLine(line)
		if err != nil {
			log.Printf("Skipping invalid log line: %v", err)
			continue
		}
		data = append(data, item)
		linesProcessed++
		if len(data) >= opts.batch {
			if err := sendData(&data); err != nil {
				return fmt.Errorf("processLogFile() on main data part: %w", err)
			}
			data = data[:0] // очищаем слайс, сохраняя capacity
		}
	}
	if len(data) > 0 {
		if err := sendData(&data); err != nil {
			return fmt.Errorf("processLogFile() on remaining data part: %w", err)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("processLogFile() scanner error: %w", err)
	}

	if linesProcessed > 0 {
		log.Printf("Processed %d log lines", linesProcessed)
	}
	return nil
}

func scheduledMagic(ctx context.Context, interval time.Duration, f func()) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.Tick(interval):
			f()
		}
	}
}

func main() {
	parseFlags()
	p := NewParser(opts.ln, opts.pn)
	if err := p.Init(); err != nil {
		log.Fatal(err)
	}
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	ex := make(chan os.Signal, 1)
	signal.Notify(ex, syscall.SIGTERM, syscall.SIGINT)
	/*
		go func() {
			for range time.Tick(10 * time.Minute) {
				hup <- syscall.SIGHUP
			}
		}()
	*/
ops:
	for {
		select {
		case <-time.Tick(opts.interval):
			{
				if err := p.Magic(); err != nil {
					log.Fatal(err)
				}
			}
		case <-hup:
			{
				time.Sleep(100 * time.Millisecond) // не торопимся
				if err := p.RotateLogFile(); err != nil {
					log.Printf("main(): %v", err)
				}
				if err := p.SavePositionFile(); err != nil {
					log.Printf("main(): %v", err)
				}
			}
		case <-ex:
			{
				if err := p.SavePositionFile(); err != nil {
					log.Fatal(err)
				}
				if err := p.Close(); err != nil {
					log.Fatal(err)
				}
				break ops
			}
		}
	}
	log.Println("Have a good day")
}
