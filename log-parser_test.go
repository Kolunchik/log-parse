package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/kolunchik/zs"
)

type ZabbixResponse struct {
	Response    string `json:"response"`
	Info        string `json:"info"`
	Processed   int    `json:"processed"`
	Failed      int    `json:"failed"`
	Total       int    `json:"total"`
	SpentMillis int    `json:"spent_millis"`
}

const (
	zabbixHeader      = "ZBXD"
	zabbixVersion     = 1
	defaultTimeout    = 5 * time.Second
	defaultRetries    = 3
	defaultRetryDelay = 1 * time.Second
)

var (
	ErrIncompleteHeader       = errors.New("incomplete header received")
	ErrInvalidProtocolVersion = errors.New("invalid protocol version")
	ErrDataLengthMismatch     = errors.New("data length mismatch")
	ErrEmptyData              = errors.New("empty data")
	ErrInvalidJSON            = errors.New("invalid JSON")
	ErrConnectionClosed       = errors.New("connection closed by server")
	ErrResponseStatus         = errors.New("zabbix response error")
)

func buildPacket(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, ErrEmptyData
	}

	buffer := new(bytes.Buffer)
	buffer.Write([]byte(zabbixHeader))
	buffer.WriteByte(zabbixVersion)
	binary.Write(buffer, binary.LittleEndian, uint64(len(data)))
	buffer.Write(data)

	return buffer.Bytes(), nil
}

func startTestServer(handler func(net.Conn)) (net.Listener, int) {
	listener, err := net.Listen("tcp", "127.0.0.1:10051")
	if err != nil {
		if _, ok := err.(net.Error); !ok {
			log.Fatal(err)
		}
		log.Println("startTestServer():", err)
		return nil, 0
	}

	go func() {
		defer listener.Close()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go handler(conn)
		}
	}()

	return listener, listener.Addr().(*net.TCPAddr).Port
}

func sendSuccessResponse(conn net.Conn) {
	response := ZabbixResponse{
		Response:  "success",
		Processed: 1,
		Info:      "Processed 1 Failed 0 Total 1",
	}
	jsonData, _ := json.Marshal(response)
	packet, _ := buildPacket(jsonData)
	conn.Write(packet)
}

var server, port = startTestServer(func(conn net.Conn) {
	sendSuccessResponse(conn)
})

func TestLine(t *testing.T) {
	expected := "2023-04-11T08:57:01.058999+03:00 10.77.2.11 %OLT: Interface EPON0/1:18's \"CTC\" OAM extension negotiated \n successfully!"
	if line() != expected {
		t.Errorf("line() returned unexpected result")
	}
}

func TestValidateLogLine(t *testing.T) {
	tests := []struct {
		name string
		line string
		want bool
	}{
		{
			name: "valid line",
			line: "2023-04-11T08:57:01.058999+03:00 10.77.2.11 message content",
			want: true,
		},
		{
			name: "IPv6 address",
			line: "2023-04-11T08:57:01.058999+03:00 2001:db8::1 message",
			want: true,
		},
		{
			name: "empty message",
			line: "2023-04-11T08:57:01.058999+03:00 10.0.0.1 ",
			want: true,
		},
		{
			name: "invalid timestamp",
			line: "invalid-time 10.77.2.11 message",
			want: false,
		},
		{
			name: "invalid IP",
			line: "2023-04-11T08:57:01.058999+03:00 invalid.ip message",
			want: false,
		},
		{
			name: "short line",
			line: "2023-04-11T08:57:01.058999+03:00",
			want: false,
		},
		{
			name: "empty line",
			line: "",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed := strings.SplitN(tt.line, " ", 3)
			if got := validateLogLine(&parsed); got != tt.want {
				t.Errorf("validateLogLine() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseLogLine(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		want    zs.ZabbixDataItem
		wantErr bool
	}{
		{
			name: "valid line",
			line: "2023-04-11T08:57:01.058999+03:00 10.77.2.11 message content",
			want: zs.ZabbixDataItem{
				Host:  "10.77.2.11",
				Key:   "test.key",
				Value: "2023-04-11T08:57:01.058999+03:00 message content",
			},
			wantErr: false,
		},
		{
			name:    "invalid line",
			line:    "invalid line format",
			want:    zs.ZabbixDataItem{},
			wantErr: true,
		},
	}

	oldKey := opts.key
	opts.key = "test.key"
	defer func() { opts.key = oldKey }()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseLogLine(tt.line)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseLogLine() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && (got.Host != tt.want.Host || got.Key != tt.want.Key || got.Value != tt.want.Value) {
				t.Errorf("parseLogLine() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestProcessLogFileErrorCases(t *testing.T) {
	// Тест на обработку ошибок сканера
	t.Run("scanner error", func(t *testing.T) {
		p := &parser{file: &os.File{}}
		err := processLogFile(p.file)
		if err == nil {
			t.Error("expected error for invalid file")
		}
	})
}

func TestRestorePositionErrors(t *testing.T) {
	t.Run("invalid position file format", func(t *testing.T) {
		tmpLog, err := os.CreateTemp("", "testlog")
		if err != nil {
			t.Fatal(err)
		}
		defer os.Remove(tmpLog.Name())

		tmpPos, err := os.CreateTemp("", "testpos")
		if err != nil {
			t.Fatal(err)
		}
		defer os.Remove(tmpPos.Name())

		// Пишем неверный формат в файл позиции
		tmpPos.WriteString("invalid data")
		tmpPos.Seek(0, 0)

		p := &parser{
			pos: tmpPos,
			ln:  tmpLog.Name(),
		}

		// Открываем файл лога
		if err := p.OpenLogFile(); err != nil {
			t.Fatalf("OpenLogFile failed: %v", err)
		}
		defer p.file.Close()

		err = p.RestorePosition()
		if err != nil {
			t.Errorf("expected to handle invalid format, got %v", err)
		}
	})

	t.Run("nil file", func(t *testing.T) {
		p := &parser{}
		err := p.RestorePosition()
		if err == nil {
			t.Error("expected error for nil file")
		} else if !strings.Contains(err.Error(), "stat log file") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("nil position file", func(t *testing.T) {
		tmpLog, err := os.CreateTemp("", "testlog")
		if err != nil {
			t.Fatal(err)
		}
		defer os.Remove(tmpLog.Name())

		p := &parser{
			file: tmpLog,
			ln:   tmpLog.Name(),
		}
		err = p.RestorePosition()
		if err == nil {
			t.Error("expected error for nil position file")
		} else if !strings.Contains(err.Error(), "seek position file") {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestFileOperationsErrors(t *testing.T) {
	t.Run("open log file error", func(t *testing.T) {
		p := NewParser("/nonexistent/file", "")
		err := p.OpenLogFile()
		if err == nil {
			t.Error("expected error for nonexistent file")
		}
	})

	t.Run("open position file error", func(t *testing.T) {
		p := NewParser("", "/invalid/path/to/position")
		err := p.OpenPositionFile()
		if err == nil {
			t.Error("expected error for invalid position file path")
		}
	})
}

func TestConcurrentHUP(t *testing.T) {
	tmpLog, err := os.CreateTemp("", "testlog")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpLog.Name())

	tmpPos, err := os.CreateTemp("", "testpos")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpPos.Name())

	oldLn := opts.ln
	oldPn := opts.pn
	oldBuf := opts.buf
	opts.ln = tmpLog.Name()
	opts.pn = tmpPos.Name()
	opts.buf = 64 * 1024
	defer func() {
		opts.ln = oldLn
		opts.pn = oldPn
		opts.buf = oldBuf
	}()

	p := NewParser(opts.ln, opts.pn)
	if err := p.Init(); err != nil {
		t.Fatalf("Init() error: %v", err)
	}
	defer p.Close()

	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.hup = true
			if err := p.RotateLogFile(); err != nil {
				t.Errorf("RotateLogFile() failed: %v", err)
			}
		}()
	}
	wg.Wait()
}

func TestParserMethods(t *testing.T) {
	tmpLog, err := os.CreateTemp("", "testlog")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpLog.Name())

	tmpPos, err := os.CreateTemp("", "testpos")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpPos.Name())

	testContent := "2023-04-11T08:57:01.058999+03:00 10.77.2.11 test message\n"
	if _, err := tmpLog.WriteString(testContent); err != nil {
		t.Fatal(err)
	}

	oldLn := opts.ln
	oldPn := opts.pn
	oldBuf := opts.buf
	oldZs := opts.zs
	oldZp := opts.zp
	opts.ln = tmpLog.Name()
	opts.pn = tmpPos.Name()
	opts.buf = 64 * 1024
	opts.zs = "127.0.0.1"
	opts.zp = 10051
	defer func() { opts.ln = oldLn; opts.pn = oldPn; opts.buf = oldBuf; opts.zs = oldZs; opts.zp = oldZp }()

	p := NewParser(opts.ln, opts.pn)

	if err := p.OpenLogFile(); err != nil {
		t.Errorf("OpenLogFile() error = %v", err)
	}

	if err := p.OpenPositionFile(); err != nil {
		t.Errorf("OpenPositionFile() error = %v", err)
	}

	if err := p.RestorePosition(); err != nil {
		t.Errorf("RestorePosition() error = %v", err)
	}

	if err := p.Magic(); err != nil {
		t.Errorf("Magic() error = %v", err)
	}

	if err := p.SavePositionFile(); err != nil {
		t.Errorf("SavePositionFile() error = %v", err)
	}

	posData, err := os.ReadFile(opts.pn)
	if err != nil {
		t.Fatal(err)
	}
	if len(posData) == 0 {
		t.Error("position file is empty after SavePositionFile")
	}

	if err := p.Close(); err != nil {
		t.Errorf("Close() error = %v", err)
	}
}

func TestInit(t *testing.T) {
	tmpLog, err := os.CreateTemp("", "testlog")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpLog.Name())

	tmpPos, err := os.CreateTemp("", "testpos")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpPos.Name())

	oldLn := opts.ln
	oldPn := opts.pn
	opts.ln = tmpLog.Name()
	opts.pn = tmpPos.Name()
	defer func() {
		opts.ln = oldLn
		opts.pn = oldPn
	}()

	p := NewParser(opts.ln, opts.pn)

	if err := p.Init(); err != nil {
		t.Errorf("Init() error = %v", err)
	}

	if p.file == nil {
		t.Error("log file not opened after Init")
	}
	if p.pos == nil {
		t.Error("position file not opened after Init")
	}

	if err := p.Close(); err != nil {
		t.Errorf("Close() error = %v", err)
	}
}

func TestProcessLogFile(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		wantErr  bool
		wantLogs int
	}{
		{
			name: "valid logs",
			content: `2023-04-11T08:57:01.058999+03:00 10.77.2.11 message1
2023-04-11T08:57:02.058999+03:00 10.77.2.12 message2
2023-04-11T08:57:03.058999+03:00 10.77.2.13 message3`,
			wantErr:  false,
			wantLogs: 3,
		},
		{
			name: "mixed valid and invalid logs",
			content: `2023-04-11T08:57:01.058999+03:00 10.77.2.11 message1
invalid line
2023-04-11T08:57:03.058999+03:00 10.77.2.13 message3`,
			wantErr:  false,
			wantLogs: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpfile, err := os.CreateTemp("", "testlog")
			if err != nil {
				t.Fatal(err)
			}
			defer os.Remove(tmpfile.Name())

			if _, err := tmpfile.WriteString(tt.content); err != nil {
				t.Fatal(err)
			}
			if _, err := tmpfile.Seek(0, io.SeekStart); err != nil {
				t.Fatal(err)
			}

			oldBatch := opts.batch
			oldBuf := opts.buf
			oldZs := opts.zs
			oldZp := opts.zp
			opts.batch = 1
			opts.buf = 64 * 1024
			opts.zs = "127.0.0.1"
			opts.zp = 10051
			defer func() { opts.batch = oldBatch; opts.buf = oldBuf; opts.zs = oldZs; opts.zp = oldZp }()

			p := NewParser(tmpfile.Name(), "")
			p.file = tmpfile

			err = processLogFile(p.file)
			if (err != nil) != tt.wantErr {
				t.Errorf("processLogFile() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSendData(t *testing.T) {
	tests := []struct {
		name    string
		data    []zs.ZabbixDataItem
		wantErr bool
	}{
		{
			name:    "empty data",
			data:    []zs.ZabbixDataItem{},
			wantErr: false,
		},
		{
			name: "valid data",
			data: []zs.ZabbixDataItem{
				{Host: "host1", Key: "key1", Value: "value1"},
			},
			wantErr: false,
		},
	}

	oldZS := opts.zs
	oldZP := opts.zp
	defer func() {
		opts.zs = oldZS
		opts.zp = oldZP
	}()

	opts.zs = "127.0.0.1"
	opts.zp = 10051

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := sendData(&tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("sendData() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLargeFileProcessing(t *testing.T) {
	tmpLog, err := os.CreateTemp("", "testlog")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpLog.Name())

	tmpPos, err := os.CreateTemp("", "testpos")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpPos.Name())

	var builder strings.Builder
	for i := range 20011 {
		builder.WriteString(fmt.Sprintf("2023-04-11T08:57:01.%06d+03:00 10.77.2.%d test message %d\n", i, i%256, i))
	}

	if _, err := tmpLog.WriteString(builder.String()); err != nil {
		t.Fatal(err)
	}

	oldLn := opts.ln
	oldPn := opts.pn
	oldBatch := opts.batch
	oldBuf := opts.buf
	oldZs := opts.zs
	oldZp := opts.zp
	opts.ln = tmpLog.Name()
	opts.pn = tmpPos.Name()
	opts.batch = 100
	opts.buf = 64 * 1024
	opts.zs = "127.0.0.1"
	opts.zp = 10051
	defer func() {
		opts.ln = oldLn
		opts.pn = oldPn
		opts.batch = oldBatch
		opts.buf = oldBuf
		opts.zs = oldZs
		opts.zp = oldZp
	}()

	p := NewParser(opts.ln, opts.pn)
	if err := p.Init(); err != nil {
		t.Fatalf("Init() error: %v", err)
	}
	defer p.Close()

	if err := p.Magic(); err != nil {
		t.Errorf("Magic() first run error = %v", err)
	}

	if err := p.Magic(); err != nil {
		t.Errorf("Magic() second run error = %v", err)
	}

	if err := p.SavePositionFile(); err != nil {
		t.Errorf("SavePositionFile() error = %v", err)
	}
}

func TestScheduledMagic(t *testing.T) {
	oldInterval := opts.interval
	opts.interval = time.Millisecond * 100
	defer func() { opts.interval = oldInterval }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	counter := 0
	f := func() {
		counter++
		if counter >= 3 {
			panic("stop")
		}
	}

	defer func() {
		if r := recover(); r != nil && r.(string) == "stop" {
			if counter != 3 {
				t.Errorf("scheduledMagic() ran %d times, want 3", counter)
			}
		}
	}()

	scheduledMagic(ctx, opts.interval, f)
}

func TestParserConcurrent(t *testing.T) {
	tmpLog, err := os.CreateTemp("", "concurrent_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpLog.Name())

	tmpPos, err := os.CreateTemp("", "concurrent_pos")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpPos.Name())

	l, _ := tmpLog.WriteString("2023-01-01T00:00:00Z 127.0.0.1 test\n")

	oldLn := opts.ln
	oldPn := opts.pn
	oldBuf := opts.buf
	oldZs := opts.zs
	oldZp := opts.zp
	opts.ln = tmpLog.Name()
	opts.pn = tmpPos.Name()
	opts.buf = 64 * 1024
	opts.zs = "127.0.0.1"
	opts.zp = 10051
	defer func() {
		opts.ln = oldLn
		opts.pn = oldPn
		opts.buf = oldBuf
		opts.zs = oldZs
		opts.zp = oldZp
	}()

	p := NewParser(opts.ln, opts.pn)
	if err := p.Init(); err != nil {
		t.Fatalf("Init() error: %v", err)
	}
	defer p.Close()

	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := p.Magic(); err != nil {
				t.Errorf("Magic() failed: %v", err)
			}
			if err := p.SavePositionFile(); err != nil {
				t.Errorf("SavePositionFile() failed: %v", err)
			}
		}()
	}
	wg.Wait()

	posData, err := os.ReadFile(opts.pn)
	if err != nil {
		t.Fatal(err)
	}
	if len(posData) == 0 {
		t.Error("position file is empty after concurrent access")
	}
	s := strings.Split(string(posData), " ")
	if fmt.Sprintf("%d", l) != fmt.Sprintf("%s", s[0]) {
		t.Errorf("position incorrect, want %v, got %v", l, s[0])
	}
}

func TestErrorCases(t *testing.T) {
	tests := []struct {
		name    string
		logPath string
		posPath string
		wantErr bool
	}{
		{
			name:    "non-existent log file",
			logPath: "/nonexistent/file",
			wantErr: true,
		},
		{
			name:    "invalid position file path",
			logPath: os.DevNull,
			posPath: "/invalid/path/to/position",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldLn := opts.ln
			oldPn := opts.pn
			opts.ln = tt.logPath
			if tt.posPath != "" {
				opts.pn = tt.posPath
			}
			defer func() {
				opts.ln = oldLn
				opts.pn = oldPn
			}()

			p := NewParser(opts.ln, opts.pn)
			err := p.Init()
			if (err != nil) != tt.wantErr {
				t.Errorf("Init() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRotateLogFile(t *testing.T) {
	tmpLog, err := os.CreateTemp("", "testlog")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpLog.Name())
	l, _ := tmpLog.WriteString("2023-01-01T00:00:00Z 127.0.0.1 test\n")
	tmpLogNew, err := os.CreateTemp("", "testlog")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpLogNew.Name())
	ln, _ := tmpLogNew.WriteString("2023-01-01T00:00:00Z 127.0.0.1 test test\n2023-01-01T00:00:00Z 127.0.0.1 test test\n")

	tmpPos, err := os.CreateTemp("", "testpos")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpPos.Name())

	oldLn := opts.ln
	oldPn := opts.pn
	oldBatch := opts.batch
	oldBuf := opts.buf
	oldZs := opts.zs
	oldZp := opts.zp
	opts.ln = tmpLog.Name()
	opts.pn = tmpPos.Name()
	opts.batch = 100
	opts.buf = 64 * 1024
	opts.zs = "127.0.0.1"
	opts.zp = 10051
	defer func() {
		opts.ln = oldLn
		opts.pn = oldPn
		opts.batch = oldBatch
		opts.buf = oldBuf
		opts.zs = oldZs
		opts.zp = oldZp
	}()

	p := NewParser(opts.ln, opts.pn)
	if err := p.Init(); err != nil {
		t.Fatalf("Init() error: %v", err)
	}
	defer p.Close()
	p.Magic()
	p.SavePositionFile()
	p.ln = tmpLogNew.Name() + " fake"
	rotated, err := p.IsRotated()
	if err == nil {
		t.Errorf("IsRotated() error = %v", err)
	}
	if rotated {
		t.Error("IsRotated() failed, got true, need false")
	}
	p.ln = tmpLogNew.Name()
	rotated, err = p.IsRotated()
	if err != nil {
		t.Errorf("IsRotated() error = %v", err)
	}
	if !rotated {
		t.Error("IsRotated() failed, got false, need true")
	}
	if err := p.RotateLogFile(); err != nil {
		t.Errorf("RotateLogFile() error = %v", err)
	}
	posData, err := os.ReadFile(opts.pn)
	if err != nil {
		t.Fatal(err)
	}
	if len(posData) == 0 {
		t.Error("position file is empty")
	}
	s := strings.Split(string(posData), " ")
	if fmt.Sprintf("%d", l) != fmt.Sprintf("%s", s[0]) {
		t.Errorf("position incorrect, want %v, got %v", l, s[0])
	}
	p.Magic()
	p.SavePositionFile()
	posDataN, err := os.ReadFile(opts.pn)
	if err != nil {
		t.Fatal(err)
	}
	if len(posDataN) == 0 {
		t.Error("position file is empty")
	}
	sN := strings.Split(string(posDataN), " ")
	if fmt.Sprintf("%d", ln) != fmt.Sprintf("%s", sN[0]) {
		t.Errorf("position incorrect, want %v, got %v", ln, sN[0])
	}
}

func TestParserHUP(t *testing.T) {
	p := NewParser("", "")
	if p.hup {
		t.Error("NewParser should set hup to false by default")
	}
}

func TestMainSignalHandling(t *testing.T) {
	// Тест можно запускать только с флагом -short
	//if !testing.Short() {
	//	t.Skip("skipping test in normal mode")
	//}

	tmpLog, err := os.CreateTemp("", "testlog")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpLog.Name())
	tmpPos, err := os.CreateTemp("", "testpos")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpPos.Name())

	oldLn := opts.ln
	oldPn := opts.pn
	opts.ln = os.DevNull
	opts.pn = os.DevNull
	defer func() {
		opts.ln = oldLn
		opts.pn = oldPn
	}()

	os.Args = append(os.Args, "-logfile", tmpLog.Name(), "-posfile", tmpPos.Name())

	// Запускаем main в отдельной goroutine
	go main()

	// Даем время на инициализацию
	time.Sleep(100 * time.Millisecond)

	// Посылаем сигналы
	proc, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}

	// Тестируем SIGHUP
	if err := proc.Signal(syscall.SIGHUP); err != nil {
		t.Errorf("SIGHUP failed: %v", err)
	}

	// Даем время на обработку
	time.Sleep(100 * time.Millisecond)

	// Тестируем SIGTERM
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		t.Errorf("SIGTERM failed: %v", err)
	}

	// Даем время на завершение
	time.Sleep(100 * time.Millisecond)
}
