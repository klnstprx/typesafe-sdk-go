package typesafe

import "time"

// Environment variables consulted by [New] when the matching option is absent.
// Empty or whitespace-only values are ignored.
const (
	// APIKeyEnv names the environment variable holding the API key.
	APIKeyEnv = "TYPESAFE_API_KEY"
	// BaseURLEnv names the environment variable holding the API base URL.
	BaseURLEnv = "TYPESAFE_BASE_URL"
	// DefaultModelEnv names the environment variable holding the default model.
	DefaultModelEnv = "TYPESAFE_DEFAULT_MODEL"
)

// Client defaults.
const (
	// DefaultBaseURL is the API root used when neither [WithBaseURL] nor [BaseURLEnv] is set.
	DefaultBaseURL = "https://api.typesafe.ai"
	// DefaultModel is the model used when neither [WithModel] nor [DefaultModelEnv] is set.
	DefaultModel = "jev-latest"
	// DefaultTimeout bounds each HTTP attempt, including reading its response body.
	DefaultTimeout = 10 * time.Second
)

const (
	sdkName            = "typesafe-sdk-go"
	systemOnePath      = "/v1/systemone"
	modelsPath         = "/v1/models"
	jsonContentType    = "application/json"
	maxErrorBodyLen    = 200
	noBodyMessage      = "status code (no body)"
	headerSDK          = "X-Typesafe-Sdk"
	headerRuntime      = "X-Typesafe-Runtime"
	headerRetryCount   = "X-Typesafe-Retry-Count"
	headerRequestID    = "X-Typesafe-Request-Id"
	headerRetryAfter   = "Retry-After"
	headerRetryAfterMS = "Retry-After-Ms"
)
