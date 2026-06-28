package services

// JobSubmitter is the interface used by the Generate, Prewarm, and PreviewStems handlers to enqueue pipeline jobs.
// *JobRunner satisfies this interface.
type JobSubmitter interface {
	Submit(videoID string, semitones int) string
	SubmitPrewarm(videoID string) string
	SubmitPreviewStems(videoID string) string
}
