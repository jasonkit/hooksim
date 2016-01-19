package types

type URLSecretPair struct {
	URL    string
	Secret string
}

type IssueActorPair struct {
	Issue []byte
	Actor []byte
}
