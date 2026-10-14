package bedrock

import (
	"regexp"
	"strings"
)

var regionRe = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]{1,2}$`)

// ValidRegion reports whether region has the shape of an AWS region name,
// so it is safe to put into a host name.
func ValidRegion(region string) bool { return regionRe.MatchString(region) }

// DNSSuffix is the endpoint domain of the region's partition.
func DNSSuffix(region string) string {
	if strings.HasPrefix(region, "cn-") {
		return "amazonaws.com.cn"
	}
	return "amazonaws.com"
}

// RuntimeURL is the Bedrock Runtime endpoint (ConverseStream) of a region.
func RuntimeURL(region string) string {
	return "https://bedrock-runtime." + region + "." + DNSSuffix(region)
}

// ControlURL is the Bedrock control-plane endpoint (model listing).
func ControlURL(region string) string {
	return "https://bedrock." + region + "." + DNSSuffix(region)
}
