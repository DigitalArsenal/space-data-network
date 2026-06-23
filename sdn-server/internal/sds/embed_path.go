package sds

import "path"

func embeddedSchemaPath(name string) string {
	return path.Join("schemas", name)
}
