# Wine Pairing Suggestions

This is a simple tool to get wine pairing suggestions for any public recipe on
the web. It leverages LLMs to read the recipe and suggest pairings that bring out
the best in that meal.

## Setting Up

_Note, this project is only in development so far._

For local development, make sure you have Go installed and run `go mod download` to
bring in dependencies.

If you want documentation for the packages, either use `go doc` or run a documentation
server on your laptop:

```bash
go install golang.org/x/tools/cmd/godoc@latest
godoc
```

The project can talk to multiple model providers. Set up at least one of the
following to use:

* AWS Bedrock
** Create an IAM role or credential that can invoke Bedrock models.
** Ensure that Claude Haiku 3.5 is available for your account.
** Add your credentials as environment variables. See `env.example`.

## Running in Development

This project contains a CLI app and a web app to drive the entire flow.

Install [Docker](https://www.docker.com/) and
[Docker Compose](https://docs.docker.com/compose/) and run the following to load the development stack:

```bash
docker-compose up -d
```

Watch development logs with:
```
docker-compose logs webapp -f
```

The development server uses [Air](https://github.com/air-verse/air)
to reload the server after file changes.

Open a browser to `http://localhost:3000` to get started.