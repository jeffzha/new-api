package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	privatePath := flag.String("private", "", "path to the PKCS#8 Ed25519 private key")
	publicPath := flag.String("public", "", "path to the PKIX Ed25519 public key")
	secretPath := flag.String("secret", "", "optional path to a 32-byte base64url secret")
	flag.Parse()
	if err := ensureKeyPair(*privatePath, *publicPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *secretPath != "" {
		if err := ensureSecret(*secretPath); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}

func ensureKeyPair(privatePath, publicPath string) error {
	if privatePath == "" || publicPath == "" || privatePath == publicPath {
		return errors.New("distinct --private and --public paths are required")
	}
	privateData, privateErr := os.ReadFile(privatePath)
	publicData, publicErr := os.ReadFile(publicPath)
	if privateErr == nil && publicErr == nil {
		privateKey, err := parsePrivateKey(privateData)
		if err != nil {
			return fmt.Errorf("read private key: %w", err)
		}
		publicKey, err := parsePublicKey(publicData)
		if err != nil {
			return fmt.Errorf("read public key: %w", err)
		}
		if !privateKey.Public().(ed25519.PublicKey).Equal(publicKey) {
			return errors.New("existing Agency Hub SSO keys do not match")
		}
		return nil
	}
	if privateErr == nil || publicErr == nil || (!os.IsNotExist(privateErr) && privateErr != nil) || (!os.IsNotExist(publicErr) && publicErr != nil) {
		return errors.New("Agency Hub SSO key pair is incomplete or unreadable; remove both local development key files and retry")
	}

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate Ed25519 key: %w", err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return fmt.Errorf("encode private key: %w", err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		return fmt.Errorf("encode public key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(privatePath), 0o700); err != nil {
		return fmt.Errorf("create private key directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(publicPath), 0o700); err != nil {
		return fmt.Errorf("create public key directory: %w", err)
	}
	if err := os.WriteFile(privatePath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}), 0o600); err != nil {
		return fmt.Errorf("write private key: %w", err)
	}
	if err := os.WriteFile(publicPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER}), 0o644); err != nil {
		_ = os.Remove(privatePath)
		return fmt.Errorf("write public key: %w", err)
	}
	return nil
}

func parsePrivateKey(data []byte) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("invalid PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok || len(key) != ed25519.PrivateKeySize {
		return nil, errors.New("key is not Ed25519")
	}
	return key, nil
}

func parsePublicKey(data []byte) (ed25519.PublicKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("invalid PEM")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(ed25519.PublicKey)
	if !ok || len(key) != ed25519.PublicKeySize {
		return nil, errors.New("key is not Ed25519")
	}
	return key, nil
}

func ensureSecret(path string) error {
	data, err := os.ReadFile(path)
	if err == nil {
		decoded, decodeErr := base64.RawURLEncoding.DecodeString(string(data))
		if decodeErr != nil || len(decoded) != 32 {
			return errors.New("existing local Agency Hub secret is invalid")
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("read local Agency Hub secret: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create secret directory: %w", err)
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return fmt.Errorf("generate local Agency Hub secret: %w", err)
	}
	if err := os.WriteFile(path, []byte(base64.RawURLEncoding.EncodeToString(secret)), 0o600); err != nil {
		return fmt.Errorf("write local Agency Hub secret: %w", err)
	}
	return nil
}
