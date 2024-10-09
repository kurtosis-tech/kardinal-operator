package topology

import (
	"fmt"
	"regexp"
)

// Helper function to create int32 pointers
func int32Ptr(i int32) *int32 {
	return &i
}

func replaceOrAddSubdomain(url string, newSubdomain string) string {
	re := regexp.MustCompile(`^(https?://)?(([^./]+\.)?([^./]+\.[^./]+))(.*)$`)
	return re.ReplaceAllString(url, fmt.Sprintf("${1}%s.${4}${5}", newSubdomain))
}
