package duckduckgo_test

import (
	"fmt"
	"testing"

	"github.com/SolaTyolo/duckduckgo"
)

func TestSearchText(t *testing.T) {
	client := duckduckgo.NewAsyncDDGS(nil, nil, 40)
	res, _ := client.Text("golang", "", "", "", "api", 10)
	fmt.Println(res)
}
