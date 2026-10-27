package leave

import (
	"context"
	"net/url"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
)

// Remotion sources (D290): pushing a version back sends its code, its words
// and its pictures to the project's repository (delivery, to that host); a
// refresh runs the source's own steps with the network on, so its files can
// go wherever they reach (like an ability: the abilities rule, and only when
// it lets any host receive them — the steps' hosts can't be held to a list).

// RemotionKinds are what a Remotion source's files carry.
var RemotionKinds = []string{models.LeaveWords, models.LeavePictures}

// RepoPush is a push of a source's version to its repository as a send.
func RepoPush(projectID, repoURL, where, what string) Send {
	host := repoURL
	if u, err := url.Parse(repoURL); err == nil && u.Hostname() != "" {
		host = u.Hostname()
	} else if i := strings.Index(repoURL, "@"); i >= 0 { // git@host:owner/repo
		host = strings.SplitN(repoURL[i+1:], ":", 2)[0]
	}
	return Send{Place: models.PlaceDelivery, To: strings.ToLower(host), ToWords: where, Kinds: RemotionKinds, Projects: []string{projectID}, What: what}
}

// Refresh is a refresh of a source as a send.
func Refresh(projectID, what string) Send {
	return Send{Place: models.PlaceAbilities, ToWords: "the places its steps reach", Kinds: RemotionKinds, Projects: []string{projectID}, What: what}
}

// KeptRefusal is the rule keeping a send here (nil: none), without recording it.
func KeptRefusal(ctx context.Context, a *app.App, s Send) *Refusal {
	r, err := Problem(ctx, a, s)
	if err != nil {
		return &Refusal{Place: s.Place, Message: "MetaRush couldn't read the client's Content Settings, so nothing goes out.", Hint: "Try again in a moment."}
	}
	return r
}
