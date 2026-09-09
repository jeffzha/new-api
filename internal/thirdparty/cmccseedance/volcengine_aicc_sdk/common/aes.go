package common

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/crypto/pbkdf2"
)

const SALT = "ai-handler-salt"

// GCMEncrypt 使用 AES-GCM 模式加密数据
func GCMEncrypt(key []byte, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	// 创建 GCM 实例
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	// 生成随机 IV
	iv := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return nil, err
	}

	// 加密
	ciphertext := gcm.Seal(nil, iv, plaintext, nil)

	// 返回 IV + 密文
	return append(iv, ciphertext...), nil
}

// GCMDecrypt 使用 AES-GCM 模式解密数据
func GCMDecrypt(key []byte, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	// 创建 GCM 实例
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	// 提取 IV
	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}

	iv, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]

	// 解密
	plaintext, err := gcm.Open(nil, iv, ciphertext, nil)
	if err != nil {
		return nil, err
	}

	return plaintext, nil
}

// GenerateKeyFromString 从字符串生成 AES 密钥
func GenerateKeyFromString(seedString string) []byte {
	salt := []byte(SALT)
	key := pbkdf2.Key([]byte(seedString), salt, 100000, 32, sha256.New)
	return key
}

// AESEncrypt 对数据进行 AES 加密，支持字符串或字节输入
func AESEncrypt(aesKey interface{}, data interface{}) ([]byte, error) {
	var key []byte
	var plaintext []byte

	// 处理密钥
	switch k := aesKey.(type) {
	case string:
		key = []byte(k)
	case []byte:
		key = k
	default:
		return nil, fmt.Errorf("invalid key type")
	}

	// 处理数据
	switch d := data.(type) {
	case string:
		plaintext = []byte(d)
	case []byte:
		plaintext = d
	default:
		return nil, fmt.Errorf("invalid data type")
	}

	return GCMEncrypt(key, plaintext)
}

// AESDecrypt 对数据进行 AES 解密，支持字符串或字节输入
func AESDecrypt(aesKey interface{}, data interface{}) ([]byte, error) {
	var key []byte
	var ciphertext []byte

	// 处理密钥
	switch k := aesKey.(type) {
	case string:
		key = []byte(k)
	case []byte:
		key = k
	default:
		return nil, fmt.Errorf("invalid key type")
	}

	// 处理数据
	switch d := data.(type) {
	case string:
		ciphertext = []byte(d)
	case []byte:
		ciphertext = d
	default:
		return nil, fmt.Errorf("invalid data type")
	}

	return GCMDecrypt(key, ciphertext)
}

// EncryptFile 加密文件，支持二进制模式和文本模式
func EncryptFile(aesKey interface{}, sourcePath, destPath, mode string) (bool, error) {
	if mode != "b" && mode != "t" {
		return false, fmt.Errorf("mode argument error")
	}

	var key []byte
	switch k := aesKey.(type) {
	case string:
		key = []byte(k)
	case []byte:
		key = k
	default:
		return false, fmt.Errorf("invalid key type")
	}

	source, err := os.Open(sourcePath)
	if err != nil {
		return false, err
	}
	defer source.Close()

	dest, err := os.Create(destPath)
	if err != nil {
		return false, err
	}
	defer dest.Close()

	if mode == "b" {
		// 二进制模式
		plaintext, err := io.ReadAll(source)
		if err != nil {
			return false, err
		}

		ciphertext, err := AESEncrypt(key, plaintext)
		if err != nil {
			return false, err
		}

		_, err = dest.Write(ciphertext)
		if err != nil {
			return false, err
		}
	} else {
		// 文本模式
		reader := bufio.NewScanner(source)
		for reader.Scan() {
			plaintext := strings.TrimRight(reader.Text(), "\n")
			ciphertext, err := AESEncrypt(key, plaintext)
			if err != nil {
				return false, err
			}

			encoded := base64.StdEncoding.EncodeToString(ciphertext)
			_, err = dest.WriteString(encoded + "\n")
			if err != nil {
				return false, err
			}
		}

		if err := reader.Err(); err != nil {
			return false, err
		}
	}

	return true, nil
}

// DecryptFile 解密文件，支持二进制模式和文本模式
func DecryptFile(aesKey interface{}, sourcePath, destPath, mode string) (bool, error) {
	if mode != "b" && mode != "t" {
		return false, fmt.Errorf("mode argument error")
	}

	var key []byte
	switch k := aesKey.(type) {
	case string:
		key = []byte(k)
	case []byte:
		key = k
	default:
		return false, fmt.Errorf("invalid key type")
	}

	source, err := os.Open(sourcePath)
	if err != nil {
		return false, err
	}
	defer source.Close()

	dest, err := os.Create(destPath)
	if err != nil {
		return false, err
	}
	defer dest.Close()

	if mode == "b" {
		// 二进制模式
		ciphertext, err := io.ReadAll(source)
		if err != nil {
			return false, err
		}

		plaintext, err := AESDecrypt(key, ciphertext)
		if err != nil {
			return false, err
		}

		_, err = dest.Write(plaintext)
		if err != nil {
			return false, err
		}
	} else {
		// 文本模式
		reader := bufio.NewScanner(source)
		for reader.Scan() {
			ciphertextStr := strings.TrimRight(reader.Text(), "\n")
			ciphertext, err := base64.StdEncoding.DecodeString(ciphertextStr)
			if err != nil {
				return false, err
			}

			plaintext, err := AESDecrypt(key, ciphertext)
			if err != nil {
				return false, err
			}

			_, err = dest.WriteString(string(plaintext) + "\n")
			if err != nil {
				return false, err
			}
		}

		if err := reader.Err(); err != nil {
			return false, err
		}
	}

	return true, nil
}
