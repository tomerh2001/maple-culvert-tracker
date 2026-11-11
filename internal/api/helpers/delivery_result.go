package helpers

// MessageReference records an actual Discord response or persisted identifier.
type MessageReference struct {
	GuildID   string `json:"guild_id"`
	ChannelID string `json:"channel_id"`
	MessageID string `json:"message_id"`
	ThreadID  string `json:"thread_id,omitempty"`
}
type DeliveryArtifactError struct {
	Cause    error
	Messages []MessageReference
}

func (e *DeliveryArtifactError) Error() string { return "delivery artifact persistence failed" }
func (e *DeliveryArtifactError) Unwrap() error { return e.Cause }
func KnownDeliveryMessages(err error) []MessageReference {
	if err == nil {
		return nil
	}
	refs := []MessageReference{}
	if e, ok := err.(*DeliveryArtifactError); ok {
		refs = append(refs, e.Messages...)
	}
	if e, ok := err.(interface{ Unwrap() []error }); ok {
		for _, child := range e.Unwrap() {
			refs = append(refs, KnownDeliveryMessages(child)...)
		}
	}
	if e, ok := err.(interface{ Unwrap() error }); ok {
		refs = append(refs, KnownDeliveryMessages(e.Unwrap())...)
	}
	return refs
}
