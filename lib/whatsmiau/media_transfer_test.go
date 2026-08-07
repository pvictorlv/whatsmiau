package whatsmiau

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/verbeux-ai/whatsmiau/env"
)

// TestFetchToTempFileStreamsToDisk trava o que conserta o envio de mídia grande:
// o conteúdo vai para disco, rebobinado, em vez de virar um []byte que a
// cifragem do upload multiplica em memória.
func TestFetchToTempFileStreamsToDisk(t *testing.T) {
	payload := bytes.Repeat([]byte("zapeada"), 300_000) // ~2 MB

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	s := &Whatsmiau{httpClient: srv.Client()}

	file, err := s.fetchToTempFile(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("fetchToTempFile failed: %v", err)
	}
	defer closeAndRemove(file)

	// Rebobinado: o chamador entrega o arquivo direto ao UploadReader, que lê do
	// ponto atual. Um arquivo no fim viraria um upload de zero byte.
	if pos, err := file.Seek(0, io.SeekCurrent); err != nil || pos != 0 {
		t.Fatalf("expected file rewound to 0, got pos %d err %v", pos, err)
	}

	got, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("failed to read temp file: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("temp file content differs from the response body (%d vs %d bytes)", len(got), len(payload))
	}
}

// TestFetchRejectsErrorStatus garante que uma página de erro do storage não seja
// tratada como o arquivo. Sem isso, um 404 do bucket subia para o WhatsApp como
// se fosse o documento.
func TestFetchRejectsErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("<Error>NoSuchKey</Error>"))
	}))
	defer srv.Close()

	s := &Whatsmiau{httpClient: srv.Client()}

	if _, err := s.fetchToTempFile(context.Background(), srv.URL); err == nil {
		t.Errorf("expected fetchToTempFile to fail on 404")
	}
	if _, err := s.fetchBytes(context.Background(), srv.URL); err == nil {
		t.Errorf("expected fetchBytes to fail on 404")
	}
}

// TestFetchToTempFileLeavesNoFileOnFailure: o temporário só existe enquanto o
// download der certo. Um erro que deixa lixo em /tmp enche o disco do servidor.
func TestFetchToTempFileLeavesNoFileOnFailure(t *testing.T) {
	before, err := os.ReadDir(os.TempDir())
	if err != nil {
		t.Skipf("cannot list temp dir: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	s := &Whatsmiau{httpClient: srv.Client()}
	if _, err := s.fetchToTempFile(context.Background(), srv.URL); err == nil {
		t.Fatalf("expected failure on 500")
	}

	after, err := os.ReadDir(os.TempDir())
	if err != nil {
		t.Skipf("cannot list temp dir: %v", err)
	}
	if len(after) > len(before) {
		t.Errorf("temp files leaked: %d before, %d after", len(before), len(after))
	}
}

func TestCanInlineBase64(t *testing.T) {
	original := env.Env.WebhookBase64MaxBytes
	defer func() { env.Env.WebhookBase64MaxBytes = original }()

	const limit = int64(24 * 1024 * 1024)
	env.Env.WebhookBase64MaxBytes = limit

	cases := []struct {
		name string
		size int64
		want bool
	}{
		{"small image rides inline", 400 * 1024, true},
		{"exactly at the limit still rides", limit, true},
		{"one byte over is fetched on demand", limit + 1, false},
		{"the 80MB pdf that broke the webhook", 80 * 1024 * 1024, false},
		{"unmeasurable file is not gambled on", -1, false},
	}

	for _, tc := range cases {
		if got := canInlineBase64(tc.size); got != tc.want {
			t.Errorf("%s: canInlineBase64(%d) = %v, want %v", tc.name, tc.size, got, tc.want)
		}
	}

	// Limite zerado desliga o corte: quem controla o consumidor pode voltar ao
	// comportamento antigo sem trocar o binário.
	env.Env.WebhookBase64MaxBytes = 0
	if !canInlineBase64(80 * 1024 * 1024) {
		t.Errorf("expected a zero limit to disable the cut")
	}
}

// TestSniffMimetypeRewinds: quem fareja o tipo lê o cabeçalho, e o upload lê
// logo depois. Sem rebobinar, o arquivo subiria sem os primeiros 512 bytes.
func TestSniffMimetypeRewinds(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "sniff-*")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer file.Close()

	pdf := append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("x"), 2048)...)
	if _, err := file.Write(pdf); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("failed to rewind: %v", err)
	}

	if got := sniffMimetype(file, ""); got != "application/pdf" {
		t.Errorf("expected application/pdf, got %q", got)
	}

	got, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("failed to read back: %v", err)
	}
	if !bytes.Equal(got, pdf) {
		t.Errorf("file not rewound: read %d of %d bytes", len(got), len(pdf))
	}
}

// TestSniffMimetypePrefersFileName: com nome de arquivo, a extensão manda — é o
// que o remetente declarou, e vale mais que o palpite pelos bytes.
func TestSniffMimetypePrefersFileName(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "sniff-*")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer file.Close()

	if _, err := file.Write([]byte("%PDF-1.7\n")); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("failed to rewind: %v", err)
	}

	if got := sniffMimetype(file, "contrato.pdf"); got != "application/pdf" {
		t.Errorf("expected application/pdf, got %q", got)
	}
}
