package common

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type TOSUtils struct {
	Workdir                   string
	DefaultLargeFileThreshold int64
	DefaultDownloadTimeout    time.Duration
	DefaultChunkSize          int
	MaxRetries                int
	RetryDelay                time.Duration
}

func NewTOSUtils(workdir string) *TOSUtils {
	if workdir == "" {
		workdir = "./tmp"
	}

	if err := os.MkdirAll(workdir, 0755); err != nil {
		fmt.Printf("Warning: Failed to create work directory: %v\n", err)
	}

	return &TOSUtils{
		Workdir:                   workdir,
		DefaultLargeFileThreshold: 100 * 1024 * 1024,
		DefaultDownloadTimeout:    300 * time.Second,
		DefaultChunkSize:          1024 * 1024,
		MaxRetries:                3,
		RetryDelay:                2 * time.Second,
	}
}

func (t *TOSUtils) DownloadSmallFile(tosURL, localFilePath string) (bool, error) {
	if tosURL == "" || localFilePath == "" {
		return false, NewAICCException("TOS URL and local file path are required")
	}

	if _, err := os.Stat(localFilePath); err == nil {
		return false, NewAICCException("Local file already exists, please delete it first")
	}

	retryDelay := t.RetryDelay

	for attempt := 1; attempt <= t.MaxRetries; attempt++ {
		fmt.Printf("Download attempt %d/%d\n", attempt, t.MaxRetries)

		success, needRetry := func() (bool, bool) {
			client := &http.Client{
				Timeout: t.DefaultDownloadTimeout,
			}

			resp, err := client.Get(tosURL)
			if err != nil {
				fmt.Printf("Download attempt %d failed: %v, try again...\n", attempt, err)
				return false, true
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				fmt.Printf("Download attempt %d failed: HTTP %d, try again...\n", attempt, resp.StatusCode)
				return false, true
			}

			out, err := os.Create(localFilePath)
			if err != nil {
				fmt.Printf("Download attempt %d failed: %v, try again...\n", attempt, err)
				return false, true
			}
			defer out.Close()

			if _, err := io.Copy(out, resp.Body); err != nil {
				fmt.Printf("Download attempt %d failed: %v, try again...\n", attempt, err)
				return false, true
			}

			fileInfo, err := os.Stat(localFilePath)
			if err != nil {
				fmt.Printf("Download attempt %d failed: %v, try again...\n", attempt, err)
				return false, true
			}

			if fileInfo.Size() == 0 {
				fmt.Printf("Download attempt %d failed: Downloaded file is empty, try again...\n", attempt)
				return false, true
			}

			fmt.Printf("Download successful, file size: %d bytes\n", fileInfo.Size())
			return true, false
		}()

		// defer 已在匿名函数返回时执行，资源已释放

		if success {
			return true, nil
		}

		if _, err := os.Stat(localFilePath); err == nil {
			os.Remove(localFilePath)
		}

		if attempt == t.MaxRetries {
			return false, NewAICCException("Failed to download file")
		}

		if needRetry {
			fmt.Printf("Waiting %v seconds before retry...\n", retryDelay)
			time.Sleep(retryDelay)
			retryDelay *= 2
		}
	}

	return false, NewAICCException("Download failed unexpectedly")
}

func (t *TOSUtils) DownloadLargeFile(tosURL, localFilePath string, expectedSize int64) (bool, error) {
	if tosURL == "" || localFilePath == "" || expectedSize <= 0 {
		return false, NewAICCException("TOS URL, local file path, and expected file size are required and must be greater than 0")
	}

	if _, err := os.Stat(localFilePath); err == nil {
		return false, NewAICCException("Local file already exists, please delete it first")
	}

	retryDelay := t.RetryDelay

	for attempt := 1; attempt <= t.MaxRetries; attempt++ {
		fmt.Printf("Streaming download attempt %d/%d\n", attempt, t.MaxRetries)

		success, needRetry := func() (bool, bool) {
			client := &http.Client{
				Timeout: t.DefaultDownloadTimeout,
			}

			resp, err := client.Get(tosURL)
			if err != nil {
				fmt.Printf("Streaming download attempt %d failed: %v\n", attempt, err)
				return false, true
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				fmt.Printf("Streaming download attempt %d failed: HTTP %d\n", attempt, resp.StatusCode)
				return false, true
			}

			out, err := os.Create(localFilePath)
			if err != nil {
				fmt.Printf("Streaming download attempt %d failed: %v\n", attempt, err)
				return false, true
			}
			defer out.Close()

			downloadedBytes := int64(0)
			buffer := make([]byte, t.DefaultChunkSize)

			for {
				n, err := resp.Body.Read(buffer)
				if err != nil && err != io.EOF {
					fmt.Printf("Streaming download attempt %d failed: %v\n", attempt, err)
					return false, true
				}

				if n == 0 {
					break
				}

				if _, err := out.Write(buffer[:n]); err != nil {
					fmt.Printf("Streaming download attempt %d failed: %v\n", attempt, err)
					return false, true
				}

				downloadedBytes += int64(n)

				if downloadedBytes%(10*1024*1024) < int64(t.DefaultChunkSize) && expectedSize > 0 {
					progress := float64(downloadedBytes) / float64(expectedSize) * 100
					fmt.Printf("Download progress: %.1f%% (%.1f MB / %.1f MB)\n",
						progress,
						float64(downloadedBytes)/(1024*1024),
						float64(expectedSize)/(1024*1024),
					)
				}
			}

			fileInfo, err := os.Stat(localFilePath)
			if err != nil {
				fmt.Printf("Streaming download attempt %d failed: %v\n", attempt, err)
				return false, true
			}

			if fileInfo.Size() == 0 {
				fmt.Printf("Streaming download attempt %d failed: Downloaded file is empty\n", attempt)
				return false, true
			}

			fmt.Printf("Download successful, file size: %d bytes\n", fileInfo.Size())
			return true, false
		}()

		// defer 已在匿名函数返回时执行，资源已释放

		if success {
			return true, nil
		}

		if _, err := os.Stat(localFilePath); err == nil {
			os.Remove(localFilePath)
		}

		if attempt == t.MaxRetries {
			return false, NewAICCException("Failed to download large file")
		}

		if needRetry {
			fmt.Printf("Waiting %v seconds before retry...\n", retryDelay)
			time.Sleep(retryDelay)
			retryDelay *= 2
		}
	}

	return false, NewAICCException("Download failed unexpectedly")
}

func (t *TOSUtils) ExtractFilenameFromURL(url string) string {
	path := strings.Split(url, "?")[0]
	filename := filepath.Base(path)
	return filename
}

func (t *TOSUtils) DownloadFile(tosURL, localFilePath, encryptedKey string) (bool, error) {
	if tosURL == "" || localFilePath == "" {
		return false, NewAICCException("TOS URL and local file path are required")
	}

	if _, err := os.Stat(localFilePath); err == nil {
		return false, NewAICCException("Local file already exists, please delete it first")
	}

	if !strings.HasPrefix(tosURL, "http") {
		return false, NewAICCException(fmt.Sprintf("Invalid TOS URL: %s\n URL must start with http:// or https://", tosURL))
	}

	success, err := t.DownloadSmallFile(tosURL, localFilePath)
	if err != nil {
		return false, err
	}

	if !success {
		return false, NewAICCException("download file from TOS failed, please check the URL")
	}

	if encryptedKey != "" {
		tmpFilePath := localFilePath + ".tmp"
		success, err := DecryptFile(encryptedKey, localFilePath, tmpFilePath, "b")
		if err != nil {
			os.Remove(localFilePath)
			return false, err
		}

		if !success {
			os.Remove(tmpFilePath)
			os.Remove(localFilePath)
			return false, NewAICCException("Failed to decrypt file")
		}

		if err := os.Remove(localFilePath); err != nil {
			os.Remove(tmpFilePath)
			return false, err
		}

		if err := os.Rename(tmpFilePath, localFilePath); err != nil {
			os.Remove(tmpFilePath)
			return false, err
		}
	}

	return true, nil
}

func (t *TOSUtils) DownloadFiles(tosURL, localFilePath string, encryptedKey []byte) (bool, error) {
	if tosURL == "" || localFilePath == "" {
		return false, NewAICCException("TOS URL and local file path are required")
	}

	if _, err := os.Stat(localFilePath); err == nil {
		return false, NewAICCException("Local file already exists, please delete it first")
	}

	if !strings.HasPrefix(tosURL, "http") {
		return false, NewAICCException(fmt.Sprintf("Invalid TOS URL: %s\n URL must start with http:// or https://", tosURL))
	}

	metaFilePath := tosURL + "/meta.json"
	client := &http.Client{
		Timeout: t.DefaultDownloadTimeout,
	}

	resp, err := client.Get(metaFilePath)
	if err != nil {
		return false, NewAICCException(fmt.Sprintf("Failed to download meta.json: %v", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, NewAICCException(fmt.Sprintf("Failed to download meta.json: HTTP %d", resp.StatusCode))
	}

	var meta struct {
		Files []string `json:"files"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return false, NewAICCException(fmt.Sprintf("Failed to parse meta.json: %v", err))
	}

	fileList := meta.Files
	if len(fileList) == 0 {
		return false, NewAICCException("No files found in the TOS directory")
	}

	sort.Strings(fileList)

	videoFiles := make([]string, 0, len(fileList))

	for _, file := range fileList {
		tosFilePath := tosURL + "/" + file
		tmpFilePath := localFilePath + "." + file

		success, err := t.DownloadSmallFile(tosFilePath, tmpFilePath)
		if err != nil {
			for _, f := range videoFiles {
				os.Remove(f)
			}
			return false, err
		}

		if !success {
			for _, f := range videoFiles {
				os.Remove(f)
			}
			return false, NewAICCException(fmt.Sprintf("download file from TOS failed, please check the URL %s", tosFilePath))
		}

		if len(encryptedKey) > 0 {
			decryptFilePath := tmpFilePath + ".dec"
			success, err := DecryptFile(encryptedKey, tmpFilePath, decryptFilePath, "b")
			if err != nil {
				for _, f := range videoFiles {
					os.Remove(f)
				}
				os.Remove(tmpFilePath)
				os.Remove(decryptFilePath)
				return false, err
			}

			if !success {
				for _, f := range videoFiles {
					os.Remove(f)
				}
				os.Remove(decryptFilePath)
				os.Remove(tmpFilePath)
				return false, NewAICCException("Failed to decrypt file")
			}

			if err := os.Remove(tmpFilePath); err != nil {
				for _, f := range videoFiles {
					os.Remove(f)
				}
				os.Remove(decryptFilePath)
				os.Remove(tmpFilePath)
				return false, err
			}

			if err := os.Rename(decryptFilePath, tmpFilePath); err != nil {
				for _, f := range videoFiles {
					os.Remove(f)
				}
				os.Remove(tmpFilePath)
				return false, err
			}
		}

		videoFiles = append(videoFiles, tmpFilePath)
	}

	out, err := os.Create(localFilePath)
	if err != nil {
		for _, f := range videoFiles {
			os.Remove(f)
		}
		os.Remove(localFilePath)
		return false, err
	}
	defer out.Close()

	for _, videoFile := range videoFiles {
		in, err := os.Open(videoFile)
		if err != nil {
			for _, f := range videoFiles {
				os.Remove(f)
			}
			os.Remove(localFilePath)
			return false, err
		}

		if _, err := io.Copy(out, in); err != nil {
			in.Close()
			for _, f := range videoFiles {
				os.Remove(f)
			}
			os.Remove(localFilePath)
			return false, err
		}

		in.Close()
		os.Remove(videoFile)
	}

	return true, nil
}
