package topology

import "encoding/json"

// Helper function to create int32 pointers
func int32Ptr(i int32) *int32 {
	return &i
}

func mustGetMarshalledKey[T any](resource T) string {
	bytes, err := json.Marshal(resource)
	if err != nil {
		panic("Failed to marshal resource")
	}
	return string(bytes)
}
