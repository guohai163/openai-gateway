package gateway

import (
	"strings"
)

func geminiModelAction(path string) (string, string, bool) {
	resource, ok := strings.CutPrefix(path, "/v1beta/models/")
	if !ok {
		return "", "", false
	}
	model, action, ok := strings.Cut(resource, ":")
	return model, action, ok && modelIdentifier.MatchString(model) &&
		!strings.ContainsAny(model, "/:") &&
		(action == "generateContent" || action == "streamGenerateContent")
}

func geminiUsage(v map[string]any) Usage {
	metadata, _ := v["usageMetadata"].(map[string]any)
	var usage Usage
	usage.Input, usage.InputKnown = tokenNumber(metadata["promptTokenCount"])
	usage.Output, usage.OutputKnown = tokenNumber(metadata["candidatesTokenCount"])
	if thought, known := tokenNumber(metadata["thoughtsTokenCount"]); known {
		usage.Output += thought
	}
	usage.Cached, usage.CachedKnown = tokenNumber(metadata["cachedContentTokenCount"])
	if metadata != nil && metadata["cachedContentTokenCount"] == nil {
		usage.CachedKnown = true
	}
	usage.ServiceTier, _ = metadata["serviceTier"].(string)
	return usage
}
