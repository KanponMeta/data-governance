data "external_schema" "ent" {
  program = [
    "go",
    "run",
    "-mod=mod",
    "ariga.io/atlas-provider-ent",
    "load",
    "--path", "./internal/storage/ent/schema",
    "--dialect", "postgres",
  ]
}

env "local" {
  src = data.external_schema.ent.url
  url = "postgres://platform_owner:platform_owner@localhost:5432/platform?sslmode=disable&search_path=public"
  dev = "docker://postgres/16/dev?search_path=public"
  migration {
    dir = "file://migrations"
  }
  format {
    migrate {
      diff = "{{ sql . \"  \" }}"
    }
  }
}

env "ci" {
  src = data.external_schema.ent.url
  url = getenv("DATABASE_URL")
  dev = "docker://postgres/16/dev?search_path=public"
  migration {
    dir = "file://migrations"
  }
}
