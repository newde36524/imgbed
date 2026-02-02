package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type UploadResponse []struct {
	Src string `json:"src"`
}

type progressReader struct {
	reader  io.Reader
	total   int64
	current int64
	start   time.Time
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.reader.Read(p)
	if n > 0 {
		pr.current += int64(n)
		pr.showProgress()
	}
	return n, err
}

func (pr *progressReader) showProgress() {
	percent := float64(pr.current) / float64(pr.total) * 100
	barWidth := 40
	filled := int(float64(barWidth) * percent / 100)
	bar := strings.Repeat("=", filled) + strings.Repeat(" ", barWidth-filled)
	elapsed := time.Since(pr.start).Seconds()
	var speed float64
	if elapsed > 0 {
		speed = float64(pr.current) / 1024 / 1024 / elapsed
	}
	fmt.Printf("\r[%s] %.1f%% (%.2f MB/%.2f MB) %.2f MB/s", bar, percent, float64(pr.current)/1024/1024, float64(pr.total)/1024/1024, speed)
}

func calculateSHA256(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

func compressFolder(folderPath string) (string, error) {
	tempFile := filepath.Join(os.TempDir(), fmt.Sprintf("%d.zip", time.Now().UnixNano()))

	zipFile, err := os.Create(tempFile)
	if err != nil {
		return "", fmt.Errorf("failed to create zip file: %v", err)
	}
	defer zipFile.Close()

	zipWriter := zip.NewWriter(zipFile)
	defer zipWriter.Close()

	err = filepath.Walk(folderPath, func(filePath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		relPath, err := filepath.Rel(folderPath, filePath)
		if err != nil {
			return err
		}

		file, err := os.Open(filePath)
		if err != nil {
			return err
		}
		defer file.Close()

		writer, err := zipWriter.Create(relPath)
		if err != nil {
			return err
		}

		_, err = io.Copy(writer, file)
		return err
	})

	if err != nil {
		os.Remove(tempFile)
		return "", fmt.Errorf("failed to compress folder: %v", err)
	}

	return tempFile, nil
}

func uploadFile(filePath string, tempFile bool) error {
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("file not found: %s", filePath)
	}

	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file: %v", err)
	}
	defer file.Close()

	sha256Hash, err := calculateSHA256(filePath)
	if err != nil {
		return fmt.Errorf("failed to calculate SHA256: %v", err)
	}

	var requestBody bytes.Buffer
	writer := multipart.NewWriter(&requestBody)

	part, err := writer.CreateFormFile("file", filepath.Base(filePath))
	if err != nil {
		return fmt.Errorf("failed to create form file: %v", err)
	}

	progressReader := &progressReader{
		reader: file,
		total:  fileInfo.Size(),
		start:  time.Now(),
	}

	if _, err := io.Copy(part, progressReader); err != nil {
		return fmt.Errorf("failed to copy file content: %v", err)
	}

	if err := writer.WriteField("sha256", sha256Hash); err != nil {
		return fmt.Errorf("failed to write sha256 field: %v", err)
	}

	if err := writer.Close(); err != nil {
		return fmt.Errorf("failed to close writer: %v", err)
	}

	fmt.Println()

	req, err := http.NewRequest("POST", "https://jmrximg.993988.xyz/upload?serverCompress=false&uploadChannel=huggingface&channelName=imgbed&uploadNameType=default&autoRetry=true&uploadFolder=", &requestBody)
	if err != nil {
		return fmt.Errorf("failed to create request: %v", err)
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("accept", "application/json, text/plain, */*")
	req.Header.Set("authcode", "jmrx")
	req.Header.Set("origin", "https://jmrximg.993988.xyz")
	req.Header.Set("referer", "https://jmrximg.993988.xyz/")
	req.Header.Set("user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/144.0.0.0 Safari/537.36")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("upload failed: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %v", err)
	}

	fmt.Printf("\nStatus: %s\n", resp.Status)

	var uploadResp UploadResponse
	if err := json.Unmarshal(body, &uploadResp); err != nil {
		fmt.Printf("Response:\n%s\n", string(body))
	} else {
		if len(uploadResp) > 0 && uploadResp[0].Src != "" {
			fullURL := "https://jmrximg.993988.xyz" + uploadResp[0].Src
			fmt.Printf("File URL: %s\n", fullURL)
		} else {
			fmt.Printf("Response:\n%s\n", string(body))
		}
	}

	if tempFile {
		if err := os.Remove(filePath); err != nil {
			fmt.Printf("Warning: failed to remove temp file: %v\n", err)
		}
	}

	return nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: imgbed <file_path_or_folder_or_text>")
		fmt.Println("  - If <file_path_or_folder_or_text> is a valid file path, upload that file")
		fmt.Println("  - If it's a folder, compress it to zip and upload")
		fmt.Println("  - Otherwise, create a text file with that content and upload it")
		os.Exit(1)
	}

	input := os.Args[1]

	info, err := os.Stat(input)
	if err == nil {
		if info.IsDir() {
			fmt.Printf("Compressing folder: %s\n", input)
			zipFile, err := compressFolder(input)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("Created zip file: %s\n", zipFile)
			fmt.Printf("Uploading...\n")
			if err := uploadFile(zipFile, true); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		} else {
			fmt.Printf("Uploading file: %s\n", input)
			if err := uploadFile(input, false); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}
	} else {
		tempFile := filepath.Join(os.TempDir(), fmt.Sprintf("%d.txt", time.Now().UnixNano()))
		if err := os.WriteFile(tempFile, []byte(input), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Error creating temp file: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Created temp file: %s\n", tempFile)
		fmt.Printf("Uploading...\n")
		if err := uploadFile(tempFile, true); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	}
}
