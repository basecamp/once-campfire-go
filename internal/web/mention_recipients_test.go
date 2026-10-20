package web

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/rails"
)

func TestMentionPreparationKeepsLegacyRecipients(t *testing.T) {
	app, _, _, user := testApp(t)
	raw, err := os.ReadFile("../../reference/vectors/rails_compat.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Secret string `json:"secret_key_base"`
		SGIDs  struct {
			Verify []struct {
				Case, SGID, Purpose, Now string
				Expected                 *string
			}
		}
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	app.ContentQueries.Secrets, err = rails.NewSecrets(fixture.Secret)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range fixture.SGIDs.Verify {
		if test.Case != "self-validated metadata (globalid < 1.0)" && test.Case != "self-validated metadata, wrong purpose" && test.Case != "self-validated metadata, expired" {
			continue
		}
		t.Run(test.Case, func(t *testing.T) {
			now, err := time.Parse(time.RFC3339Nano, test.Now)
			if err != nil {
				t.Fatal(err)
			}
			app.DB.Now = func() time.Time { return now }
			body := fmt.Sprintf(`<action-text-attachment sgid="%s"></action-text-attachment>`, test.SGID)
			ids, err := app.ContentQueries.MentionedIDs(context.Background(), app.presentationFacts(context.Background()), body)
			if err != nil {
				t.Fatal(err)
			}
			if test.Expected == nil {
				if len(ids) != 0 {
					t.Fatal("invalid recipient accepted", ids)
				}
			} else if len(ids) != 1 || ids[0] != user.ID {
				t.Fatal("legacy recipient lost during database preparation", ids)
			}
		})
	}
}
