package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/kolunchik/zs"
)

// TestLine проверяет пример строки лога
func TestLine(t *testing.T) {
	expected := "2023-04-11T08:57:01.058999+03:00 10.77.2.11 %OLT: Interface EPON0/1:18's \"CTC\" OAM extension negotiated \n successfully!"
	if line() != expected {
		t.Errorf("line() returned unexpected result")
	}
}

// TestValidateLogLine проверяет валидацию строк лога
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

// TestParseLogLine проверяет парсинг строк лога
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

	// Сохраняем и восстанавливаем значение opts.key
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

// TestProcessLogFile проверяет обработку лог-файла
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

	// Mock sendData для тестов
	//oldSendData := sendData
	//defer func() { sendData = oldSendData }()
	//sendData = func(data *[]zs.ZabbixDataItem) error {
	//	return nil
	//}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Создаем временный файл с тестовыми логами
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

			// Сохраняем и восстанавливаем значение opts.batch
			oldBatch := opts.batch
			opts.batch = 1 // Уменьшаем batch для тестирования отправки
			opts.buf = 1000
			opts.zs = "127.0.0.1"
			opts.zp = 10051
			defer func() { opts.batch = oldBatch }()

			err = processLogFile(tmpfile)
			if (err != nil) != tt.wantErr {
				t.Errorf("processLogFile() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestPositionFileHandling проверяет обработку файла позиции
func TestPositionFileHandling(t *testing.T) {
	tests := []struct {
		name        string
		initialPos  string
		logSize     int64
		expectedPos int64
		shouldReset bool
	}{
		{
			name:        "valid position",
			initialPos:  "100",
			logSize:     200,
			expectedPos: 100,
			shouldReset: false,
		},
		{
			name:        "position larger than file",
			initialPos:  "300",
			logSize:     200,
			expectedPos: 0,
			shouldReset: true,
		},
		{
			name:        "invalid position format",
			initialPos:  "invalid",
			logSize:     200,
			expectedPos: 0,
			shouldReset: true,
		},
		{
			name:        "empty position file",
			initialPos:  "",
			logSize:     200,
			expectedPos: 0,
			shouldReset: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Создаем временный файл позиции
			tmpPosFile, err := os.CreateTemp("", "testpos")
			if err != nil {
				t.Fatal(err)
			}
			defer os.Remove(tmpPosFile.Name())

			if _, err := tmpPosFile.WriteString(tt.initialPos); err != nil {
				t.Fatal(err)
			}
			if _, err := tmpPosFile.Seek(0, io.SeekStart); err != nil {
				t.Fatal(err)
			}

			// Создаем временный лог-файл
			tmpLogFile, err := os.CreateTemp("", "testlog")
			if err != nil {
				t.Fatal(err)
			}
			defer os.Remove(tmpLogFile.Name())

			if err := tmpLogFile.Truncate(tt.logSize); err != nil {
				t.Fatal(err)
			}

			// Сохраняем оригинальные значения
			oldPn := opts.pn
			oldLn := opts.ln
			opts.pn = tmpPosFile.Name()
			opts.ln = tmpLogFile.Name()
			defer func() {
				opts.pn = oldPn
				opts.ln = oldLn
			}()

			// Открываем файлы как в main()
			logfile, err := os.Open(opts.ln)
			if err != nil {
				t.Fatal(err)
			}
			defer logfile.Close()

			logfilestat, err := logfile.Stat()
			if err != nil {
				t.Fatal(err)
			}

			posfile, err := os.OpenFile(opts.pn, os.O_RDWR|os.O_CREATE, 0644)
			if err != nil {
				t.Fatal(err)
			}
			defer posfile.Close()

			// Читаем позицию
			var position int64
			if _, err := fmt.Fscanf(posfile, "%d", &position); err != nil || position > logfilestat.Size() {
				if !tt.shouldReset {
					t.Errorf("unexpected error or position reset: %v", err)
				}
				position = 0
			}

			if tt.shouldReset && position != 0 {
				t.Errorf("expected position to be reset to 0, got %d", position)
			} else if !tt.shouldReset && position != tt.expectedPos {
				t.Errorf("expected position %d, got %d", tt.expectedPos, position)
			}
		})
	}
}

// TestSendData проверяет отправку данных
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

	// Сохраняем оригинальные значения
	oldZS := opts.zs
	oldZP := opts.zp
	defer func() {
		opts.zs = oldZS
		opts.zp = oldZP
	}()

	// Настраиваем тестовый сервер
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

func TestMagick(t *testing.T) {
	parseFlags()
	if err := magic(); err != nil {
		t.Errorf("magic() returned unexpected result: %v", err)
	}
}
