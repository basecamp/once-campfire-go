package storage

import (
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
)

// URL generation consumes metadata and signing configuration only. It has no
// byte-store or persistence dependency.
func SignedID(verifier rails.Verifier, b database.Blob) string {
	token, err := verifier.Generate(b.ID, "blob_id", time.Time{})
	if err != nil {
		panic(err)
	}
	return token
}

func BlobURL(verifier rails.Verifier, b database.Blob) string {
	return "/rails/active_storage/blobs/redirect/" + Escape(
		SignedID(verifier, b),
		false,
	) + "/" + Escape(
		Filename(b.Filename),
		true,
	)
}

func RepresentationURL(verifier rails.Verifier, b database.Blob, v Variation) (string, error) {
	// ActiveStorage::Blob#variation defaults the format before signing the URL.
	if v.Get("format") == nil {
		v = append(Variation{{Key: "format", Value: DefaultFormat(b)}}, v...)
	}
	raw, err := v.MarshalJSON()
	if err != nil {
		return "", err
	}
	key, err := verifier.GenerateRaw(raw, "variation", time.Time{})
	if err != nil {
		return "", err
	}
	return "/rails/active_storage/representations/redirect/" + Escape(
		SignedID(verifier, b),
		false,
	) + "/" + Escape(
		key,
		false,
	) + "/" + Escape(
		Filename(b.Filename),
		true,
	), nil
}
