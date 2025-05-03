package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"net/netip"
	"os"
	"strings"
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
	flag.StringVar(&opts.pn, "posname", "/var/tmp/log-parser.txt", "file to store position")
	flag.StringVar(&opts.ln, "logname", "/var/log/net.log", "path to logfile")
	flag.StringVar(&opts.key, "key", "net.log", "key for zabbix trapper")
	flag.StringVar(&opts.zs, "zabbix-server", "127.0.0.1", "zabbix server address")
	flag.IntVar(&opts.zp, "zabbix-port", 10051, "zabbix server port")
	flag.IntVar(&opts.buf, "buffer-size", 64*1024, "read buffer size in bytes")
	flag.IntVar(&opts.batch, "batch-size", 100, "max items per batch")
	flag.DurationVar(&opts.interval, "interval", 0, "interval between iterations")
	flag.Parse()
}

func sendData(data *[]zs.ZabbixDataItem) error {
	if len(*data) < 1 {
		return nil
	}
	sender := zs.NewSender(opts.zs, opts.zp)
	resp, err := sender.Send(*data)
	log.Println("Zabbix response:", resp)
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
		return zs.ZabbixDataItem{}, fmt.Errorf("invalid log format: %q", line)
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
				return fmt.Errorf("failed to send data: %v", err)
			}
			data = data[:0] // очищаем слайс, сохраняя capacity
		}
	}
	if len(data) > 0 {
		if err := sendData(&data); err != nil {
			return fmt.Errorf("failed to send remaining data: %v", err)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scanner error: %v", err)
	}

	log.Printf("Processed %d log lines", linesProcessed)
	return nil
}

func magic() error {
	logfile, err := os.Open(opts.ln)
	if err != nil {
		return fmt.Errorf("Failed to open log file: %v", err)
	}
	defer logfile.Close()
	logfilestat, err := logfile.Stat()
	if err != nil {
		return fmt.Errorf("Failed to get log file stats: %v", err)
	}
	posfile, err := os.OpenFile(opts.pn, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return fmt.Errorf("Failed to open position file: %v", err)
	}
	defer posfile.Close()
	var position int64
	if _, err := fmt.Fscanf(posfile, "%d", &position); err != nil || position > logfilestat.Size() {
		position = 0
	}
	log.Printf("Position %v, logfile size %v, date %v", position, logfilestat.Size(), logfilestat.ModTime())
	if _, err = logfile.Seek(position, io.SeekStart); err != nil {
		return fmt.Errorf("Failed to seek log file: %v", err)
	}
	if err := processLogFile(logfile); err != nil {
		return fmt.Errorf("Log processing failed: %v", err)
	}
	lastposition, err := logfile.Seek(0, io.SeekCurrent)
	if err != nil {
		return fmt.Errorf("Failed to get current position: %v", err)
	}
	if position == lastposition {
		return nil
	}
	if err := posfile.Truncate(0); err != nil {
		return fmt.Errorf("Failed to truncate position file: %v", err)
	}
	if _, err := posfile.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("Failed to seek position file: %v", err)
	}
	if _, err := fmt.Fprintf(posfile, "%d", lastposition); err != nil {
		return fmt.Errorf("Failed to write position: %v", err)
	}
	log.Printf("New position saved: %d", lastposition)
	return nil
}

func scheduledMagick() {
	for range time.Tick(opts.interval) {
		if err := magic(); err != nil {
			log.Fatal(err)
		}
	}
}

func main() {
	parseFlags()
	if err := magic(); err != nil {
		log.Fatal(err)
	}
	if opts.interval > 0 {
		scheduledMagick()
	}
}
