package env

import (
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
)

type E struct {
	Port           string `env:"PORT" envDefault:"8080"`
	DebugMode      bool   `env:"DEBUG_MODE" envDefault:"false"`
	DebugWhatsmeow bool   `env:"DEBUG_WHATSMEOW" envDefault:"false"`

	RedisURL      string `env:"REDIS_URL" envDefault:"localhost:6379"`
	RedisPassword string `env:"REDIS_PASSWORD"`
	RedisTLS      bool   `env:"REDIS_TLS" envDefault:"false"`
	RedisDB       int    `env:"REDIS_DB" envDefault:"0"` // isola o whatsmiau num DB lógico próprio quando o Redis é compartilhado

	ApiKey    string `env:"API_KEY" envDefault:""`
	DBDialect string `env:"DIALECT_DB" envDefault:"sqlite3"`                   // sqlite3 or postgres
	DBURL     string `env:"DB_URL" envDefault:"file:data.db?_foreign_keys=on"` // "postgres://<user>:<pass>@<host>:<port>/<DB>?sslmode=disable

	GCSEnabled bool   `env:"GCS_ENABLED" envDefault:"false"`
	GCSBucket  string `env:"GCS_BUCKET" envDefault:"whatsmiau"`
	GCSURL     string `env:"GCS_URL" envDefault:"https://storage.googleapis.com"`

	// Storage S3-compatível (R2, MinIO, AWS). Os nomes são iguais aos da
	// evolution-api de propósito: o bloco do `.env` dela serve aqui sem
	// tradução, e as duas gravam no mesmo bucket.
	//
	// Com storage ligado a mídia recebida vai do CDN direto para o bucket em
	// streaming e o webhook carrega só a URL — o base64 sai do caminho.
	S3Enabled   bool   `env:"S3_ENABLED" envDefault:"false"`
	S3AccessKey string `env:"S3_ACCESS_KEY"`
	S3SecretKey string `env:"S3_SECRET_KEY"`
	S3Endpoint  string `env:"S3_ENDPOINT"`
	S3Port      int    `env:"S3_PORT" envDefault:"443"`
	S3UseSSL    bool   `env:"S3_USE_SSL" envDefault:"true"`
	S3Region    string `env:"S3_REGION"`
	S3Bucket    string `env:"S3_BUCKET"`
	// S3PublicURL é a base servida publicamente. Vazia, o objeto é entregue por
	// URL assinada, que expira e por isso não serve para mídia que fica no
	// histórico do ticket.
	S3PublicURL string `env:"S3_PUBLIC_URL"`
	// S3Prefix separa os objetos do whatsmiau dos da evolution no mesmo bucket
	// (ela usa `evolution-api`). ATENÇÃO: regra de expiração do bucket costuma
	// ser por prefixo — se a sua expira só `evolution-api/`, aponte isto para lá
	// ou crie a regra equivalente, senão a mídia do whatsmiau nunca é apagada.
	S3Prefix string `env:"S3_PREFIX" envDefault:"whatsmiau"`
	// S3PresignExpiry vale só quando não há S3PublicURL.
	S3PresignExpiry time.Duration `env:"S3_PRESIGN_EXPIRY" envDefault:"168h"`

	GCL          string `json:"GCL_APP_NAME" envDefault:"whatsmiau-br-1"`
	GCLEnabled   bool   `json:"GCL_ENABLED" envDefault:"false"`
	GCLProjectID string `json:"GCL_PROJECT_ID"`

	EmitterBufferSize    int `env:"EMITTER_BUFFER_SIZE" envDefault:"2048"`
	HandlerSemaphoreSize int `env:"HANDLER_SEMAPHORE_SIZE" envDefault:"512"`
	EmitterWorkers       int `env:"EMITTER_WORKERS" envDefault:"50"`

	ProxyAddresses []string `env:"PROXY_ADDRESSES" envDefault:""`      // random choices proxies ex: <SOCKS5|HTTP|HTTPS>://<username>:<password>@<host>:<port>
	ProxyStrategy  string   `env:"PROXY_STRATEGY" envDefault:"RANDOM"` // todo: implement BALANCED
	ProxyNoMedia   bool     `env:"PROXY_NO_MEDIA" envDefault:"false"`

	// Rotating proxy pool read from a file (same format as the Evolution API):
	// a JSON array/{"proxies": []} or one proxy URL per line. Instances without
	// their own proxy take one from this pool, and the file is re-read whenever
	// it changes, so proxies can be added or removed without a restart.
	ProxyPoolFile     string        `env:"PROXY_POOL_FILE" envDefault:""`
	ProxyPoolRotation string        `env:"PROXY_POOL_ROTATION" envDefault:"round_robin"` // round_robin | random | sticky
	ProxyPoolCooldown time.Duration `env:"PROXY_POOL_COOLDOWN" envDefault:"5m"`          // quarantine applied to a proxy after a connection failure

	ManagerURL string `env:"MANAGER_URL" envDefault:""`

	// Transferência de mídia. Um único teto de tempo servia webhook, chamadas de
	// controle E download de arquivo, então o valor tinha que ser curto — e um
	// valor curto mata o download de um PDF de 80 MB no meio. Cada uso tem o seu.
	//
	// MediaTransferTimeout é o teto de ponta a ponta para baixar a mídia de
	// origem antes de subi-la ao WhatsApp. É um limite de segurança, não uma
	// espera: transferência normal termina muito antes.
	MediaTransferTimeout time.Duration `env:"MEDIA_TRANSFER_TIMEOUT" envDefault:"10m"`
	// MediaEventTimeout é o orçamento de conversão de uma mensagem recebida,
	// que inclui baixar a mídia do CDN e subi-la ao storage.
	MediaEventTimeout time.Duration `env:"MEDIA_EVENT_TIMEOUT" envDefault:"10m"`
	// WebhookTimeout é o teto de uma tentativa de entrega de webhook. Payload com
	// mídia inline é grande, e 10s não davam nem para o corpo sair.
	WebhookTimeout time.Duration `env:"WEBHOOK_TIMEOUT" envDefault:"60s"`
	// WebhookBase64MaxBytes é o tamanho máximo de mídia embutida como base64 no
	// webhook. Acima disso o consumidor busca sob demanda: base64 infla 33%, e um
	// corpo de ~107 MB estoura o limite de JSON do consumidor e trava a fila de
	// eventos. 0 desliga o corte.
	WebhookBase64MaxBytes int64 `env:"WEBHOOK_BASE64_MAX_BYTES" envDefault:"25165824"` // 24 MiB
}

var Env E

func Load() error {
	_ = godotenv.Load(".env")
	err := env.Parse(&Env)

	return err
}
