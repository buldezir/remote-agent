package model

import (
	"reflect"
	"testing"
)

func TestSessionMentions(t *testing.T) {
	text := "compare @[Fix login](session:0193-a) with @[Old (v1)](session:0193-b), then @[Fix login](session:0193-a) again; not @[x](other:1) or me@[y](session:"
	want := []SessionMention{{"Fix login", "0193-a"}, {"Old (v1)", "0193-b"}}
	if got := SessionMentions(text); !reflect.DeepEqual(got, want) {
		t.Errorf("mentions = %+v", got)
	}
	if got := MentionDisplay("see @[Fix login](session:0193-a)."); got != "see @Fix login." {
		t.Errorf("display = %q", got)
	}
	if got := SearchText(&Item{Kind: ItemUserMessage, Text: "see @[Fix login](session:0193-a)"}); got != "see @Fix login" {
		t.Errorf("search text = %q", got)
	}
}
