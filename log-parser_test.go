package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestValidateLogLineEdgeCases(t *testing.T) {
	tests := []struct {
		name string
		line string
		want bool
	}{
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

// TestLargeFileProcessing проверяет обработку больших файлов
func TestLargeFileProcessing(t *testing.T) {
	parseFlags()
	// Сохраняем оригинальные значения
	oldLn := opts.ln
	oldPn := opts.pn
	oldBatch := opts.batch
	defer func() {
		opts.ln = oldLn
		opts.pn = oldPn
		opts.batch = oldBatch
	}()

	// Создаем временные файлы
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

	// Генерируем большой файл (10010 строк)
	var builder strings.Builder
	for i := range 10010 {
		builder.WriteString(fmt.Sprintf("2023-04-11T08:57:01.%06d+03:00 10.77.2.%d test message %d\n", i, i%256, i))
	}

	if _, err := tmpLog.WriteString(builder.String()); err != nil {
		t.Fatal(err)
	}

	// Устанавливаем тестовые параметры
	opts.ln = tmpLog.Name()
	opts.pn = tmpPos.Name()
	opts.batch = 101 // Обрабатываем по 100 строк за раз

	if err := magic(); err != nil {
		t.Errorf("magic() with large file error = %v", err)
	}
}

// TestMagicErrorCases проверяет обработку ошибок в magic()
func TestMagicErrorCases(t *testing.T) {
	// Сохраняем оригинальные значения
	oldLn := opts.ln
	oldPn := opts.pn
	defer func() {
		opts.ln = oldLn
		opts.pn = oldPn
	}()

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
			opts.ln = tt.logPath
			if tt.posPath != "" {
				opts.pn = tt.posPath
			}

			if err := magic(); (err != nil) != tt.wantErr {
				t.Errorf("magic() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestMagicFileOperations проверяет работу с файлами в magic()
func TestMagicFileOperations(t *testing.T) {
	// Сохраняем оригинальные значения
	oldLn := opts.ln
	oldPn := opts.pn
	defer func() {
		opts.ln = oldLn
		opts.pn = oldPn
	}()

	// Создаем временные файлы
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

	// Записываем тестовые данные
	testContent := "2023-04-11T08:57:01.058999+03:00 10.77.2.11 test message\n"
	if _, err := tmpLog.WriteString(testContent); err != nil {
		t.Fatal(err)
	}

	// Устанавливаем тестовые пути
	opts.ln = tmpLog.Name()
	opts.pn = tmpPos.Name()

	// Первый запуск - должен обработать весь файл
	if err := magic(); err != nil {
		t.Errorf("magic() first run error = %v", err)
	}

	// Проверяем содержимое позиционного файла
	posContent, err := os.ReadFile(tmpPos.Name())
	if err != nil {
		t.Fatal(err)
	}
	if len(posContent) == 0 {
		t.Error("position file is empty after magic()")
	}

	// Второй запуск - не должен ничего обрабатывать (файл не изменился)
	if err := magic(); err != nil {
		t.Errorf("magic() second run error = %v", err)
	}
}

// TestScheduledMagic проверяет работу планировщика
func TestScheduledMagic(t *testing.T) {
	oldInterval := opts.interval
	defer func() { opts.interval = oldInterval }()

	opts.interval = time.Millisecond * 100
	counter := 0
	f := func() {
		counter++
		if counter >= 3 {
			panic("stop") // Это остановит тест после 3 итераций
		}
	}

	defer func() {
		if r := recover(); r != nil && r.(string) == "stop" {
			if counter != 3 {
				t.Errorf("scheduledMagic() ran %d times, want 3", counter)
			}
		}
	}()

	scheduledMagic(f)
}

func TestMagicConcurrent(t *testing.T) {
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

	// Пишем тестовые данные
	_, _ = tmpLog.WriteString("2023-01-01T00:00:00Z 127.0.0.1 test\n")

	// Подменяем глобальные настройки
	oldLn, oldPn := opts.ln, opts.pn
	opts.ln, opts.pn = tmpLog.Name(), tmpPos.Name()
	defer func() { opts.ln, opts.pn = oldLn, oldPn }()

	// Запускаем 10 горутин
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := magic(); err != nil {
				t.Errorf("magic() failed: %v", err)
			}
		}()
	}
	wg.Wait()

	// Проверяем, что позиция записана корректно
	posData, err := os.ReadFile(tmpPos.Name())
	if err != nil {
		t.Fatal(err)
	}
	if len(posData) == 0 {
		t.Error("position file is empty after concurrent access")
	}
}
