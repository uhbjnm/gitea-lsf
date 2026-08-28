#!/usr/bin/env python3

import argparse
import http.client
import json
import os
import pathlib
import sys
import urllib.error
import urllib.parse
import urllib.request


def api_request(base_url, token, method, path, payload=None):
    data = None
    headers = {
        "Accept": "application/json",
        "Authorization": f"token {token}",
    }
    if payload is not None:
        data = json.dumps(payload, separators=(",", ":")).encode()
        headers["Content-Type"] = "application/json"

    request = urllib.request.Request(
        urllib.parse.urljoin(base_url + "/", path.lstrip("/")),
        data=data,
        headers=headers,
        method=method,
    )
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            body = response.read()
    except urllib.error.HTTPError as error:
        body = error.read(4096)
        try:
            message = json.loads(body).get("message", error.reason)
        except (json.JSONDecodeError, AttributeError):
            message = error.reason
        raise RuntimeError(f"Gitea API {method} {path} failed: HTTP {error.code}: {message}") from None

    if not body:
        return None
    return json.loads(body)


def upload_file(upload_url, headers, file_path):
    parsed = urllib.parse.urlsplit(upload_url)
    if parsed.scheme == "https":
        connection = http.client.HTTPSConnection(parsed.hostname, parsed.port, timeout=60)
    elif parsed.scheme == "http":
        connection = http.client.HTTPConnection(parsed.hostname, parsed.port, timeout=60)
    else:
        raise RuntimeError("unsupported upload URL scheme")

    target = urllib.parse.urlunsplit(("", "", parsed.path, parsed.query, ""))
    request_headers = {str(name): str(value) for name, value in headers.items()}
    request_headers["Content-Length"] = str(file_path.stat().st_size)

    try:
        connection.putrequest("PUT", target)
        for name, value in request_headers.items():
            connection.putheader(name, value)
        connection.endheaders()
        with file_path.open("rb") as source:
            while chunk := source.read(1024 * 1024):
                connection.send(chunk)
        response = connection.getresponse()
        response.read()
        if response.status < 200 or response.status >= 300:
            raise RuntimeError(f"OSS upload failed for {file_path.name}: HTTP {response.status}")
    except (OSError, http.client.HTTPException) as error:
        raise RuntimeError(f"OSS upload failed for {file_path.name}: {error}") from None
    finally:
        connection.close()


def parse_args():
    parser = argparse.ArgumentParser(description="Publish a Gitea Release with direct OSS asset uploads")
    parser.add_argument("--url", default=os.environ.get("GITEA_URL"), help="Gitea base URL; defaults to GITEA_URL")
    parser.add_argument("--owner", required=True)
    parser.add_argument("--repo", required=True)
    parser.add_argument("--tag", required=True)
    parser.add_argument("--target", default="main")
    parser.add_argument("--title")
    parser.add_argument("--notes", default="")
    parser.add_argument("--notes-file", type=pathlib.Path)
    parser.add_argument("--prerelease", action="store_true")
    parser.add_argument("files", nargs="+", type=pathlib.Path)
    return parser.parse_args()


def main():
    args = parse_args()
    token = os.environ.get("GITEA_TOKEN", "").strip()
    if not args.url:
        raise RuntimeError("GITEA_URL or --url is required")
    if not token:
        raise RuntimeError("GITEA_TOKEN is required")

    base_url = args.url.rstrip("/")
    title = args.title or args.tag
    notes = args.notes_file.read_text(encoding="utf-8") if args.notes_file else args.notes
    files = [path.resolve(strict=True) for path in args.files]
    if any(path.is_dir() for path in files):
        raise RuntimeError("release assets must be files")
    if len({path.name for path in files}) != len(files):
        raise RuntimeError("release asset file names must be unique")

    owner = urllib.parse.quote(args.owner, safe="")
    repo = urllib.parse.quote(args.repo, safe="")
    api_base = f"/api/v1/repos/{owner}/{repo}"
    api_request(base_url, token, "GET", "/api/v1/user")
    repository = api_request(base_url, token, "GET", api_base)
    permissions = repository.get("permissions") or {}
    if not (permissions.get("push") or permissions.get("admin")):
        raise RuntimeError("GITEA_TOKEN does not have write permission for the repository")

    release = api_request(
        base_url,
        token,
        "POST",
        f"{api_base}/releases",
        {
            "tag_name": args.tag,
            "target_commitish": args.target,
            "name": title,
            "body": notes,
            "draft": True,
            "prerelease": args.prerelease,
        },
    )
    release_id = int(release["id"])

    try:
        for file_path in files:
            upload = api_request(
                base_url,
                token,
                "POST",
                f"/{owner}/{repo}/releases/attachments/direct",
                {
                    "release_id": release_id,
                    "name": file_path.name,
                    "size": file_path.stat().st_size,
                },
            )
            upload_file(upload["upload"]["href"], upload["upload"].get("header", {}), file_path)
            api_request(
                base_url,
                token,
                "POST",
                upload["complete_url"],
                {"token": upload["token"]},
            )

        assets = api_request(base_url, token, "GET", f"{api_base}/releases/{release_id}/assets")
        expected = {(path.name, path.stat().st_size) for path in files}
        actual = {(asset["name"], int(asset["size"])) for asset in assets}
        missing = expected - actual
        if missing:
            raise RuntimeError("Gitea did not report all expected release assets")

        published = api_request(
            base_url,
            token,
            "PATCH",
            f"{api_base}/releases/{release_id}",
            {
                "tag_name": release["tag_name"],
                "target_commitish": release["target_commitish"],
                "name": release["name"],
                "body": release["body"],
                "draft": False,
                "prerelease": bool(release["prerelease"]),
            },
        )
    except Exception as error:
        raise RuntimeError(f"release {release_id} remains a draft: {error}") from None

    print(
        json.dumps(
            {
                "id": int(published["id"]),
                "tag": published["tag_name"],
                "url": published["html_url"],
                "assets": len(files),
            },
            separators=(",", ":"),
        )
    )


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print(str(error), file=sys.stderr)
        raise SystemExit(1)
