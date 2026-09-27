package config

// Preset is a ready provider entry for a service models.toml users commonly add
// (REQ-LLM-007). examples/models.toml carries each one, and a test keeps the two in step.
type Preset struct {
	Name        string
	Description string
	Provider    Provider
}

// Presets are the provider presets of REQ-LLM-007. Both go through openai-compat: neither
// needs an adapter of its own, which is the point of offering them.
var Presets = []Preset{
	{
		Name: "Hugging Face router",
		Description: "One OpenAI-compatible endpoint in front of many inference providers; a " +
			"`:<provider>` suffix on the model pins the backend.",
		Provider: Provider{
			ID: "hf", Type: "openai-compat", BaseURL: "https://router.huggingface.co/v1",
			Credential: CredentialRef{Kind: CredentialKeyring, Ref: "umbral/huggingface"},
		},
	},
	{
		Name: "OmniRoute",
		Description: "A self-hosted OpenAI-compatible gateway with intent aliases and its own " +
			"quota fallback. On loopback Umbral counts it as local, so offline mode does not " +
			"stop it and what it forwards is not in egress_log.",
		Provider: Provider{
			ID: "omniroute", Type: "openai-compat", BaseURL: "http://127.0.0.1:20128/v1",
			Credential: CredentialRef{Kind: CredentialNone},
		},
	},
}
