package tokens

import (
	"sync"

	"github.com/tiktoken-go/tokenizer"
)

var (
	once   sync.Once
	enc    tokenizer.Codec
	encErr error
)

func Count(s string) (int, error) {
	once.Do(func() {
		enc, encErr = tokenizer.Get(tokenizer.Cl100kBase)
	})
	if encErr != nil {
		return 0, encErr
	}
	ids, _, err := enc.Encode(s)
	if err != nil {
		return 0, err
	}
	return len(ids), nil
}
