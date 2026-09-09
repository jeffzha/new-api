package common

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net/url"
	"os"
	"time"
)

// FileToBase64 将文件转换为base64编码
func FileToBase64(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", WrapError(err, "open file failed")
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		return "", WrapError(err, "read file failed")
	}

	return base64.StdEncoding.EncodeToString(data), nil
}

// Base64ToFile 将base64编码转换为文件
func Base64ToFile(base64Data, outputFilePath string) error {
	if base64Data == "" {
		return NewAICCException("no base64 data to write")
	}

	data, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil {
		return WrapError(err, "decode base64 failed")
	}

	err = os.WriteFile(outputFilePath, data, 0644)
	if err != nil {
		return WrapError(err, "write file failed")
	}

	fmt.Printf("successfully wrote base64 data to file: %s\n", outputFilePath)
	return nil
}

// FormatQuery 格式化查询参数
func FormatQuery(params map[string]string) string {
	values := url.Values{}
	for k, v := range params {
		values.Add(k, v)
	}
	return values.Encode()
}

// GenerateUUID 生成UUID
func GenerateUUID() string {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	if err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
