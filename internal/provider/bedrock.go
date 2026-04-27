package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrock"
	"github.com/aws/aws-sdk-go-v2/service/bedrock/types"
)

// BedrockNativeConfig configures the native Bedrock passthrough at /bedrock.
// Clients use boto3 (or any other AWS SDK) and goop signs the request with
// SigV4 on their behalf, so a single shared bearer is enough at the edge.
type BedrockNativeConfig struct {
	Region string

	// CrossRegionPrefix prepends an inference profile to model IDs when
	// listing (e.g. "us" → "us.anthropic.claude-3-haiku-..."). Empty disables.
	CrossRegionPrefix string

	// AWSConfig overrides the default AWS config; nil means LoadDefaultConfig.
	AWSConfig *aws.Config
}

// NewBedrockNative builds the /bedrock passthrough. The proxy hands clients
// raw access to bedrock-runtime; goop only fixes the URL host and signs.
func NewBedrockNative(cfg BedrockNativeConfig) (Provider, error) {
	awsCfg, region, err := loadAWS(cfg.AWSConfig, cfg.Region)
	if err != nil {
		return nil, err
	}
	host := fmt.Sprintf("bedrock-runtime.%s.amazonaws.com", region)
	upstream := &url.URL{Scheme: "https", Host: host}

	return &bedrockNative{
		region:    region,
		crPrefix:  cfg.CrossRegionPrefix,
		upstream:  upstream,
		awsConfig: awsCfg,
		transport: newSigV4Transport(awsCfg.Credentials, region, "bedrock", nil),
		ctlClient: bedrock.NewFromConfig(awsCfg),
	}, nil
}

type bedrockNative struct {
	region    string
	crPrefix  string
	upstream  *url.URL
	awsConfig aws.Config
	transport http.RoundTripper
	ctlClient *bedrock.Client
}

func (p *bedrockNative) Name() string                 { return "bedrock" }
func (p *bedrockNative) Prefix() string               { return "/bedrock" }
func (p *bedrockNative) Transport() http.RoundTripper { return p.transport }

func (p *bedrockNative) Rewrite(pr *httputil.ProxyRequest) {
	pr.SetURL(p.upstream)
	pr.SetXForwarded()

	rest := strings.TrimPrefix(pr.In.URL.Path, "/bedrock")
	pr.Out.URL.Path = singleSlash(rest)
	pr.Out.URL.RawPath = ""

	// SigV4 transport will strip and re-set headers; we just remove the
	// goop-side bearer here for clarity.
	pr.Out.Header.Del("Authorization")
}

func (p *bedrockNative) ListModels(ctx context.Context) ([]Model, error) {
	out, err := p.ctlClient.ListFoundationModels(ctx, &bedrock.ListFoundationModelsInput{})
	if err != nil {
		return nil, fmt.Errorf("bedrock: list foundation models: %w", err)
	}
	models := make([]Model, 0, len(out.ModelSummaries))
	now := time.Now().Unix()
	for _, m := range out.ModelSummaries {
		if m.ModelLifecycle == nil || m.ModelLifecycle.Status != types.FoundationModelLifecycleStatusActive {
			continue
		}
		if !hasOnDemand(m.InferenceTypesSupported) {
			continue
		}
		id := safe(m.ModelId)
		owner := safe(m.ProviderName)
		models = append(models, Model{
			ID:       "bedrock/" + id,
			Object:   "model",
			Created:  now,
			OwnedBy:  owner,
			Provider: "bedrock",
		})
		if p.crPrefix != "" {
			models = append(models, Model{
				ID:       fmt.Sprintf("bedrock/%s.%s", p.crPrefix, id),
				Object:   "model",
				Created:  now,
				OwnedBy:  owner,
				Provider: "bedrock",
			})
		}
	}
	return models, nil
}

// BedrockMantleConfig configures Bedrock's OpenAI-compat (Mantle) endpoint at
// /bedrock-openai. SigV4-signed but speaks OpenAI wire format, so we
// piggy-back on the OpenAI-compat provider with a SigV4 transport.
type BedrockMantleConfig struct {
	Region    string
	AWSConfig *aws.Config
}

// NewBedrockMantle builds the OpenAI-compatible Bedrock provider.
func NewBedrockMantle(cfg BedrockMantleConfig) (Provider, error) {
	awsCfg, region, err := loadAWS(cfg.AWSConfig, cfg.Region)
	if err != nil {
		return nil, err
	}
	base, err := url.Parse(fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com/openai", region))
	if err != nil {
		return nil, err
	}
	transport := newSigV4Transport(awsCfg.Credentials, region, "bedrock", nil)
	return NewOpenAICompat(OpenAICompatConfig{
		Name:          "bedrock-openai",
		Prefix:        "/bedrock-openai",
		BaseURL:       base,
		// Bedrock Mantle does not expose /openai/v1/models. Listing is served
		// by the native bedrock provider via ListFoundationModels instead.
		ModelsPath:    "",
		ModelIDPrefix: "bedrock/",
		HTTPTransport: transport,
		// SigV4 fills in Authorization itself; no static auth header here.
	})
}

func loadAWS(in *aws.Config, region string) (aws.Config, string, error) {
	var cfg aws.Config
	var err error
	if in != nil {
		cfg = *in
	} else {
		cfg, err = awsconfig.LoadDefaultConfig(context.Background())
		if err != nil {
			return aws.Config{}, "", fmt.Errorf("aws: load config: %w", err)
		}
	}
	if region == "" {
		region = cfg.Region
	}
	if region == "" {
		region = "us-east-1"
	}
	cfg.Region = region
	return cfg, region, nil
}

func hasOnDemand(supported []types.InferenceType) bool {
	for _, t := range supported {
		if t == types.InferenceTypeOnDemand {
			return true
		}
	}
	return false
}

func safe(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
