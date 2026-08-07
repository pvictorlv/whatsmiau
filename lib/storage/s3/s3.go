// Package s3 guarda a mídia num bucket S3-compatível (R2, MinIO, AWS).
//
// Existe para tirar o base64 do caminho: com storage ligado, a mídia recebida
// vai do CDN do WhatsApp direto para o bucket em streaming e o webhook carrega
// só a URL. Sem storage, o único jeito de entregar o arquivo é embutir base64
// no evento, que infla o conteúdo em um terço e passa inteiro pela memória dos
// dois lados.
//
// A configuração usa os mesmos nomes de variável da evolution-api, e o prefixo
// do objeto é o que separa os dois serviços dentro de um bucket compartilhado.
package s3

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/verbeux-ai/whatsmiau/env"
	"github.com/verbeux-ai/whatsmiau/interfaces"
	"golang.org/x/net/context"
)

var _ interfaces.Storage = (*S3)(nil)

type S3 struct {
	client    *minio.Client
	bucket    string
	prefix    string
	publicURL string
}

func New() (*S3, error) {
	if env.Env.S3Bucket == "" {
		return nil, errors.New("S3_BUCKET is required when S3_ENABLED is true")
	}
	if env.Env.S3Endpoint == "" {
		return nil, errors.New("S3_ENDPOINT is required when S3_ENABLED is true")
	}

	endpoint := env.Env.S3Endpoint
	// A porta faz parte do endereço para o MinIO; num endpoint gerenciado
	// (R2, AWS) ela é a padrão do esquema e anexá-la só atrapalha a assinatura.
	if port := env.Env.S3Port; port != 0 && port != 80 && port != 443 && !strings.Contains(endpoint, ":") {
		endpoint = fmt.Sprintf("%s:%d", endpoint, port)
	}

	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(env.Env.S3AccessKey, env.Env.S3SecretKey, ""),
		Secure: env.Env.S3UseSSL,
		Region: env.Env.S3Region,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create the s3 client: %w", err)
	}

	return &S3{
		client:    client,
		bucket:    env.Env.S3Bucket,
		prefix:    strings.Trim(env.Env.S3Prefix, "/"),
		publicURL: strings.TrimRight(env.Env.S3PublicURL, "/"),
	}, nil
}

// objectName aplica o prefixo do serviço. É ele que impede a mídia do whatsmiau
// de colidir com a da evolution num bucket compartilhado.
func (s *S3) objectName(fileName string) string {
	fileName = strings.TrimLeft(fileName, "/")
	if s.prefix == "" {
		return fileName
	}
	return path.Join(s.prefix, fileName)
}

// url devolve o endereço público do objeto. Sem S3_PUBLIC_URL cai na URL
// assinada, que expira — serve para diagnóstico, não para mídia que precisa
// continuar abrindo no histórico do ticket meses depois.
func (s *S3) url(ctx context.Context, objectName string) (string, error) {
	if s.publicURL != "" {
		return s.publicURL + "/" + objectName, nil
	}

	signed, err := s.client.PresignedGetObject(ctx, s.bucket, objectName, env.Env.S3PresignExpiry, nil)
	if err != nil {
		return "", fmt.Errorf("failed to build the object url: %w", err)
	}
	return signed.String(), nil
}

// readerSize descobre o tamanho sem ler o conteúdo. Importa porque com tamanho
// desconhecido o cliente sobe em partes de tamanho fixo e precisa bufferizar
// uma parte inteira; com o tamanho na mão ele transmite direto do arquivo.
func readerSize(file io.Reader) int64 {
	stater, ok := file.(interface{ Stat() (os.FileInfo, error) })
	if !ok {
		return -1
	}
	info, err := stater.Stat()
	if err != nil {
		return -1
	}
	return info.Size()
}

func (s *S3) Upload(ctx context.Context, fileName, mimetype string, file io.Reader) (string, string, error) {
	objectName := s.objectName(fileName)

	if _, err := s.client.PutObject(ctx, s.bucket, objectName, file, readerSize(file), minio.PutObjectOptions{
		ContentType: mimetype,
	}); err != nil {
		return "", "", fmt.Errorf("failed to upload %s: %w", objectName, err)
	}

	url, err := s.url(ctx, objectName)
	if err != nil {
		return "", "", err
	}

	return url, fileName, nil
}

// UploadBase64IfDontExists atende as fotos de perfil, que são pequenas, chegam
// já em base64 e se repetem a cada evento do mesmo contato — daí o atalho de
// não reenviar o que já está lá.
//
// O nome vem do chamador e é derivado do contato, então é estável: é isso que
// faz o "if don't exists" valer alguma coisa. Gerar um nome novo a cada chamada
// tornaria a verificação decorativa e reenviaria a mesma foto para sempre.
func (s *S3) UploadBase64IfDontExists(ctx context.Context, fileName, mimetype, b64 string) (string, error) {
	decoded, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("failed to decode the base64 payload: %w", err)
	}

	fileName = withExtension(fileName, mimetype, decoded)
	if mimetype == "" {
		mimetype = mime.TypeByExtension(filepath.Ext(fileName))
	}

	if _, err := s.client.StatObject(ctx, s.bucket, s.objectName(fileName), minio.StatObjectOptions{}); err == nil {
		return s.url(ctx, s.objectName(fileName))
	}

	url, _, err := s.Upload(ctx, fileName, mimetype, bytes.NewReader(decoded))
	if err != nil {
		return "", err
	}

	return url, nil
}

// preferredExtensions fixa a extensão dos tipos que realmente aparecem aqui.
//
// mime.ExtensionsByType lê a tabela do sistema operacional e devolve a lista em
// ordem arbitrária: para image/jpeg o Windows entrega ".jfif" primeiro e o Linux
// ".jpe". A extensão vai dentro da URL que fica gravada no ticket, então deixá-la
// depender da máquina que compilou o binário não é aceitável.
var preferredExtensions = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
	"image/gif":  ".gif",
}

// extensionFor resolve a extensão de um mimetype, preferindo a tabela fixa.
func extensionFor(mimetype string) string {
	mimetype = strings.TrimSpace(strings.SplitN(mimetype, ";", 2)[0])
	if ext, ok := preferredExtensions[mimetype]; ok {
		return ext
	}
	if exts, _ := mime.ExtensionsByType(mimetype); len(exts) > 0 {
		return exts[0]
	}
	return ""
}

// withExtension garante a extensão do arquivo antes de gravá-lo. Objeto sem
// extensão chega ao navegador sem tipo e a foto não renderiza.
func withExtension(fileName, mimetype string, content []byte) string {
	if filepath.Ext(fileName) != "" {
		return fileName
	}

	ext := extensionFor(mimetype)
	if ext == "" {
		sample := content
		if len(sample) > 512 {
			sample = sample[:512]
		}
		ext = extensionFor(http.DetectContentType(sample))
	}

	if fileName == "" {
		return uuid.NewString() + ext
	}
	return fileName + ext
}
