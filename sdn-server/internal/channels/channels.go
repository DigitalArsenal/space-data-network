// Package channels defines the public SDN channel naming contract.
package channels

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

const DiscoveryTopicPrefix = "/spacedatanetwork/channels/"

var (
	uuidPattern          = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	standardCodePattern  = regexp.MustCompile(`^[A-Z]{3}$`)
	internalSchemaSuffix = string([]byte{'.', 'f', 'b', 's'})
)

type ChannelID struct {
	ChannelID    string
	SourceID     string
	StandardCode string
	FeedUUID     string
}

type ChannelIDInput struct {
	SourceID     string
	StandardCode string
	FeedUUID     string
}

func ParseChannelID(channelID string) (ChannelID, error) {
	value := strings.TrimSpace(channelID)
	if value == "" || strings.Contains(value, "--") {
		return ChannelID{}, fmt.Errorf("invalid channel ID %q", channelID)
	}
	parts := strings.Split(value, "-")
	if len(parts) < 2 {
		return ChannelID{}, fmt.Errorf("invalid channel ID %q", channelID)
	}
	for _, part := range parts {
		if part == "" {
			return ChannelID{}, fmt.Errorf("invalid channel ID %q", channelID)
		}
	}

	standardIndex := len(parts) - 1
	feedUUID := ""
	if len(parts) >= 7 {
		maybeUUID := strings.Join(parts[len(parts)-5:], "-")
		if uuidPattern.MatchString(maybeUUID) {
			feedUUID = maybeUUID
			standardIndex = len(parts) - 6
		}
	}
	if standardIndex <= 0 {
		return ChannelID{}, fmt.Errorf("channel sourceId is required in %q", channelID)
	}

	standardCode, err := AssertStandardCode(parts[standardIndex])
	if err != nil {
		return ChannelID{}, err
	}
	sourceID := strings.Join(parts[:standardIndex], "-")
	formatted, err := FormatChannelID(ChannelIDInput{
		SourceID:     sourceID,
		StandardCode: standardCode,
		FeedUUID:     feedUUID,
	})
	if err != nil {
		return ChannelID{}, err
	}
	return ChannelID{
		ChannelID:    formatted,
		SourceID:     sourceID,
		StandardCode: standardCode,
		FeedUUID:     feedUUID,
	}, nil
}

func FormatChannelID(input ChannelIDInput) (string, error) {
	sourceID := strings.TrimSpace(input.SourceID)
	if sourceID == "" || strings.Contains(sourceID, "--") {
		return "", fmt.Errorf("channel sourceId is required")
	}
	standardCode, err := AssertStandardCode(input.StandardCode)
	if err != nil {
		return "", err
	}
	feedUUID := strings.TrimSpace(input.FeedUUID)
	if feedUUID != "" && !uuidPattern.MatchString(feedUUID) {
		return "", fmt.Errorf("invalid feedUuid %q", input.FeedUUID)
	}
	if feedUUID != "" {
		return sourceID + "-" + standardCode + "-" + feedUUID, nil
	}
	return sourceID + "-" + standardCode, nil
}

func AssertStandardCode(value string) (string, error) {
	code := strings.TrimSpace(value)
	if !standardCodePattern.MatchString(code) {
		return "", fmt.Errorf("invalid standardCode %q", value)
	}
	if err := sds.ValidateSchemaName(code + internalSchemaSuffix); err != nil {
		return "", fmt.Errorf("invalid standardCode %q: %w", value, err)
	}
	for _, schemaName := range sds.SupportedSchemas {
		if schemaName == code+internalSchemaSuffix {
			return code, nil
		}
	}
	if sds.IsPublishedBindingSchema(code + internalSchemaSuffix) {
		return code, nil
	}
	return "", fmt.Errorf("unknown standardCode %q", value)
}

func SchemaNameFromStandardCode(standardCode string) (string, error) {
	code, err := AssertStandardCode(standardCode)
	if err != nil {
		return "", err
	}
	return code + internalSchemaSuffix, nil
}

func StandardCodeFromSchemaName(schemaName string) (string, error) {
	value := strings.TrimSpace(schemaName)
	if !strings.HasSuffix(value, internalSchemaSuffix) {
		return "", fmt.Errorf("invalid schema name %q", schemaName)
	}
	return AssertStandardCode(strings.TrimSuffix(value, internalSchemaSuffix))
}

func DiscoveryTopic(standardCode string) string {
	code, err := AssertStandardCode(standardCode)
	if err != nil {
		return ""
	}
	return DiscoveryTopicPrefix + code
}
