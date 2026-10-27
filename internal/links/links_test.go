package links

import "testing"

func TestLinks(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{Asset("", "ast_1"), "/?asset=ast_1"},
		{Asset("prj_1", "ast_1"), "/p/prj_1?asset=ast_1&tab=sources"},
		{AssetSpeakers("", "ast_1"), "/?asset=ast_1&panel=speakers"},
		{AssetSpeakers("prj_1", "ast_1"), "/p/prj_1?asset=ast_1&panel=speakers&tab=sources"},
		{Todos(""), "/todos"},
		{Todos("ast_1"), "/todos?asset=ast_1"},
		{Recipe(""), "/library/recipes"},
		{Recipe("app-teaser"), "/library/recipes?recipe=app-teaser"},
		{RecipeRun("rrn_1"), "/library/recipes?run=rrn_1"},
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}
