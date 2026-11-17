package common

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"mime/multipart"
	"net"
	"net/smtp"
	"slices"
	"strings"
	"time"
)

// SMTPConfigured reports whether the current settings are sufficient to make
// a delivery attempt. Authenticated and trusted relay configurations are both
// supported, but partially configured credentials are rejected.
func SMTPConfigured() bool {
	server := strings.TrimSpace(SMTPServer)
	from := strings.TrimSpace(SMTPFrom)
	account := strings.TrimSpace(SMTPAccount)
	token := strings.TrimSpace(SMTPToken)
	if from == "" {
		from = account
	}
	if server == "" || SMTPPort <= 0 || SMTPPort > 65535 || Validate.Var(from, "required,email") != nil {
		return false
	}
	return (account == "" && token == "") || (account != "" && token != "")
}

func EmailVerificationRequired() bool {
	return EmailVerificationEnabled && SMTPConfigured()
}

func generateMessageID() (string, error) {
	split := strings.Split(SMTPFrom, "@")
	if len(split) < 2 {
		return "", fmt.Errorf("invalid SMTP account")
	}
	domain := strings.Split(SMTPFrom, "@")[1]
	return fmt.Sprintf("<%d.%s@%s>", time.Now().UnixNano(), GetRandomString(12), domain), nil
}

func shouldUseSMTPLoginAuth() bool {
	if SMTPForceAuthLogin {
		return true
	}
	return isOutlookServer(SMTPAccount) || slices.Contains(EmailLoginAuthServerList, SMTPServer)
}

func getSMTPAuth() smtp.Auth {
	return AutoSMTPAuth(SMTPAccount, SMTPToken)
}

func shouldAuthenticateSMTP() bool {
	return SMTPAccount != "" && SMTPToken != ""
}

func smtpTLSConfig() *tls.Config {
	return &tls.Config{
		ServerName:         SMTPServer,
		InsecureSkipVerify: SMTPInsecureSkipVerify, // #nosec G402 -- admin-controlled SMTP compatibility option.
	}
}

func newSMTPClient(addr string) (*smtp.Client, error) {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	if SMTPSSLEnabled || (SMTPPort == 465 && !SMTPStartTLSEnabled) {
		conn, err := tls.DialWithDialer(dialer, "tcp", addr, smtpTLSConfig())
		if err != nil {
			return nil, err
		}
		if err := conn.SetDeadline(time.Now().Add(45 * time.Second)); err != nil {
			_ = conn.Close()
			return nil, err
		}
		client, err := smtp.NewClient(conn, SMTPServer)
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		return client, nil
	}

	conn, err := dialer.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	if err := conn.SetDeadline(time.Now().Add(45 * time.Second)); err != nil {
		_ = conn.Close()
		return nil, err
	}
	client, err := smtp.NewClient(conn, SMTPServer)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}

	if SMTPStartTLSEnabled {
		startTLSSupported, _ := client.Extension("STARTTLS")
		if !startTLSSupported {
			_ = client.Close()
			return nil, fmt.Errorf("SMTP server does not support STARTTLS")
		}
		if err := client.StartTLS(smtpTLSConfig()); err != nil {
			_ = client.Close()
			return nil, err
		}
	}

	return client, nil
}

type EmailAttachment struct {
	Filename    string
	ContentType string
	Data        []byte
}

func SendEmail(subject string, receiver string, content string) error {
	return SendEmailWithAttachments(subject, receiver, content, nil)
}

// SendEmailWithAttachments preserves the existing SMTP configuration path and
// adds RFC 2046 multipart attachments for invoices and other audit documents.
func SendEmailWithAttachments(subject string, receiver string, content string, attachments []EmailAttachment) error {
	if !SMTPConfigured() {
		return fmt.Errorf("SMTP 服务器未配置完整")
	}
	if SMTPFrom == "" { // for compatibility
		SMTPFrom = SMTPAccount
	}
	id, err2 := generateMessageID()
	if err2 != nil {
		return err2
	}
	encodedSubject := fmt.Sprintf("=?UTF-8?B?%s?=", base64.StdEncoding.EncodeToString([]byte(subject)))
	var body bytes.Buffer
	contentType := "text/html; charset=UTF-8"
	if len(attachments) > 0 {
		mw := multipart.NewWriter(&body)
		contentType = "multipart/mixed; boundary=" + mw.Boundary()
		part, err := mw.CreatePart(map[string][]string{
			"Content-Type":              {"text/html; charset=UTF-8"},
			"Content-Transfer-Encoding": {"8bit"},
		})
		if err != nil {
			return err
		}
		if _, err := part.Write([]byte(content)); err != nil {
			return err
		}
		for _, attachment := range attachments {
			if strings.TrimSpace(attachment.Filename) == "" || len(attachment.Data) == 0 {
				continue
			}
			attachmentType := strings.TrimSpace(attachment.ContentType)
			if attachmentType == "" {
				attachmentType = "application/octet-stream"
			}
			part, err := mw.CreatePart(map[string][]string{
				"Content-Type":              {attachmentType + "; name=\"" + attachment.Filename + "\""},
				"Content-Disposition":       {"attachment; filename=\"" + attachment.Filename + "\""},
				"Content-Transfer-Encoding": {"base64"},
			})
			if err != nil {
				return err
			}
			encoded := make([]byte, base64.StdEncoding.EncodedLen(len(attachment.Data)))
			base64.StdEncoding.Encode(encoded, attachment.Data)
			for start := 0; start < len(encoded); start += 76 {
				end := start + 76
				if end > len(encoded) {
					end = len(encoded)
				}
				if _, err := part.Write(append(encoded[start:end], '\r', '\n')); err != nil {
					return err
				}
			}
		}
		if err := mw.Close(); err != nil {
			return err
		}
	} else {
		body.WriteString(content)
	}
	mail := []byte(fmt.Sprintf("To: %s\r\n"+
		"From: %s <%s>\r\n"+
		"Subject: %s\r\n"+
		"Date: %s\r\n"+
		"Message-ID: %s\r\n"+
		"MIME-Version: 1.0\r\n"+
		"Content-Type: %s\r\n\r\n%s\r\n",
		receiver, SystemName, SMTPFrom, encodedSubject, time.Now().Format(time.RFC1123Z), id, contentType, body.String()))
	auth := getSMTPAuth()
	addr := fmt.Sprintf("%s:%d", SMTPServer, SMTPPort)
	to := strings.Split(receiver, ";")
	var err error
	client, err := newSMTPClient(addr)
	if err != nil {
		return err
	}
	defer client.Close()
	if shouldAuthenticateSMTP() {
		if err = client.Auth(auth); err != nil {
			return err
		}
	}
	if err = client.Mail(SMTPFrom); err != nil {
		return err
	}
	for _, receiver := range to {
		if err = client.Rcpt(receiver); err != nil {
			return err
		}
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	_, err = w.Write(mail)
	if err != nil {
		return err
	}
	err = w.Close()
	if err != nil {
		return err
	}
	err = client.Quit()
	if err != nil {
		SysError(fmt.Sprintf("failed to send email to %s: %v", receiver, err))
	}
	return err
}
