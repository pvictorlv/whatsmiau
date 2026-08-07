package s3

import (
	"strings"
	"testing"

	"github.com/verbeux-ai/whatsmiau/env"
)

func TestObjectNameAppliesPrefix(t *testing.T) {
	cases := []struct {
		name     string
		prefix   string
		fileName string
		want     string
	}{
		// O prefixo é o que separa a mídia do whatsmiau da da evolution
		// (que grava sob "evolution-api") num bucket compartilhado.
		{"prefixo padrão", "whatsmiau", "wpp_1/abc.pdf", "whatsmiau/wpp_1/abc.pdf"},
		{"barra sobrando não duplica", "whatsmiau", "/wpp_1/abc.pdf", "whatsmiau/wpp_1/abc.pdf"},
		{"sem prefixo grava na raiz", "", "wpp_1/abc.pdf", "wpp_1/abc.pdf"},
	}

	for _, tc := range cases {
		s := &S3{prefix: tc.prefix}
		if got := s.objectName(tc.fileName); got != tc.want {
			t.Errorf("%s: objectName(%q) = %q, want %q", tc.name, tc.fileName, got, tc.want)
		}
	}
}

func TestURLUsesPublicBase(t *testing.T) {
	s := &S3{publicURL: "https://midia.paralela.ai"}

	got, err := s.url(nil, "whatsmiau/wpp_1/abc.pdf")
	if err != nil {
		t.Fatalf("url failed: %v", err)
	}
	if got != "https://midia.paralela.ai/whatsmiau/wpp_1/abc.pdf" {
		t.Errorf("unexpected url: %q", got)
	}
}

// TestWithExtension: objeto sem extensão chega ao navegador sem tipo e a foto
// de perfil não renderiza.
func TestWithExtension(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("x", 64))

	cases := []struct {
		name     string
		fileName string
		mimetype string
		content  []byte
		want     string
	}{
		{"extensão presente é respeitada", "5511999999999.jpg", "image/jpeg", nil, "5511999999999.jpg"},
		{"extensão vem do mimetype", "5511999999999", "image/jpeg", nil, "5511999999999.jpg"},
		{"sem mimetype, fareja o conteúdo", "5511999999999", "", png, "5511999999999.png"},
	}

	for _, tc := range cases {
		if got := withExtension(tc.fileName, tc.mimetype, tc.content); got != tc.want {
			t.Errorf("%s: withExtension(%q, %q) = %q, want %q", tc.name, tc.fileName, tc.mimetype, got, tc.want)
		}
	}

	// Nome vazio ainda precisa virar um objeto válido.
	if got := withExtension("", "image/jpeg", nil); !strings.HasSuffix(got, ".jpg") || len(got) <= len(".jpg") {
		t.Errorf("expected a generated name ending in .jpg, got %q", got)
	}
}

// TestWithExtensionIsStable trava o que faz o "if don't exists" funcionar: o
// mesmo contato precisa resolver sempre para o mesmo objeto, senão a
// verificação de existência nunca casa e a foto é reenviada para sempre.
func TestWithExtensionIsStable(t *testing.T) {
	first := withExtension("5511999999999", "image/jpeg", nil)
	second := withExtension("5511999999999", "image/jpeg", nil)

	if first != second {
		t.Errorf("expected a stable object name, got %q then %q", first, second)
	}
}

func TestNewRequiresBucketAndEndpoint(t *testing.T) {
	original := env.Env
	defer func() { env.Env = original }()

	env.Env.S3Enabled = true
	env.Env.S3Bucket = ""
	env.Env.S3Endpoint = "s3.example.com"
	if _, err := New(); err == nil {
		t.Errorf("expected an error without S3_BUCKET")
	}

	env.Env.S3Bucket = "paralela"
	env.Env.S3Endpoint = ""
	if _, err := New(); err == nil {
		t.Errorf("expected an error without S3_ENDPOINT")
	}
}

// TestNewKeepsManagedEndpointClean: anexar :443 num endpoint gerenciado quebra
// a assinatura da requisição, então a porta só entra quando é fora do padrão.
func TestNewKeepsManagedEndpointClean(t *testing.T) {
	original := env.Env
	defer func() { env.Env = original }()

	env.Env.S3Enabled = true
	env.Env.S3Bucket = "paralela"
	env.Env.S3Region = "auto"
	env.Env.S3UseSSL = true
	env.Env.S3Endpoint = "conta.r2.cloudflarestorage.com"
	env.Env.S3Port = 443

	client, err := New()
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	if got := client.client.EndpointURL().Host; got != "conta.r2.cloudflarestorage.com" {
		t.Errorf("expected the endpoint untouched, got %q", got)
	}

	// MinIO auto-hospedado, onde a porta faz parte do endereço.
	env.Env.S3Endpoint = "minio.interno"
	env.Env.S3Port = 9000
	env.Env.S3UseSSL = false

	client, err = New()
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	if got := client.client.EndpointURL().Host; got != "minio.interno:9000" {
		t.Errorf("expected the port appended, got %q", got)
	}
}
