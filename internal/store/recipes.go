package store

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// migrateRecipes adds the Recipes feature's records (D201): one short name per workspace, their
// versions by number, and runs listed per recipe, per project and by state
// (the loop that advances runs reads the running and waiting ones).
func migrateRecipes(ctx context.Context, d *DB) error {
	if err := createIndexes(ctx, d, CRecipes,
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "name", Value: 1}}, options.Index().SetUnique(true)),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "updatedAt", Value: -1}}),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "projectId", Value: 1}})); err != nil {
		return fmt.Errorf("%s indexes: %w", CRecipes, err)
	}
	if err := createIndexes(ctx, d, CRecipeVersions,
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "recipeId", Value: 1}, {Key: "number", Value: -1}})); err != nil {
		return fmt.Errorf("%s indexes: %w", CRecipeVersions, err)
	}
	return createIndexes(ctx, d, CRecipeRuns,
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "recipeId", Value: 1}, {Key: "number", Value: -1}}, options.Index().SetUnique(true)),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "state", Value: 1}, {Key: "updatedAt", Value: -1}}),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "projectId", Value: 1}, {Key: "createdAt", Value: -1}}))
}

// migrateRecipePacks finds runs by the projects of their pack's shorts (D214):
// a short is a project of its own, and its page says which run made it.
func migrateRecipePacks(ctx context.Context, d *DB) error {
	return createIndexes(ctx, d, CRecipeRuns,
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "outputs.projectId", Value: 1}}, options.Index().SetSparse(true)))
}
