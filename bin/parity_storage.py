"""Storage HTTP and persisted ownership workflows for check-parity."""

import base64
import hashlib
import html
import json
import re
import time
import urllib.parse
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


def allocate_blob(app, content, filename, content_type, form=False):
    fields = {
        "filename": filename,
        "content_type": content_type,
        "byte_size": len(content),
        "checksum": base64.b64encode(hashlib.md5(content).digest()).decode(),
    }
    body = (
        urllib.parse.urlencode(
            {f"blob[{key}]": value for key, value in fields.items()}
        ).encode()
        if form
        else json.dumps({"blob": fields}).encode()
    )
    status, _, _, data = app.fetch(
        "kevin",
        "POST",
        "/rails/active_storage/direct_uploads",
        body,
        {
            "Content-Type": "application/x-www-form-urlencoded"
            if form
            else "application/json",
            "Accept": "application/json",
        },
    )
    assert status == 200, ("allocate", form, status, data)
    blob = json.loads(data)
    uploaded = urllib.parse.urlsplit(blob["direct_upload"]["url"])
    assert (uploaded.scheme, uploaded.netloc) == (
        urllib.parse.urlsplit(app.base).scheme,
        urllib.parse.urlsplit(app.base).netloc,
    )
    blob["upload_path"] = uploaded.path
    blob["proxy_path"] = (
        f"/rails/active_storage/blobs/proxy/{blob['signed_id']}/{filename}"
    )
    assert app.rows(
        "SELECT filename,content_type,byte_size,checksum FROM active_storage_blobs WHERE id=?",
        (blob["id"],),
    ) == [tuple(fields.values())]
    assert (
        app.rows(
            "SELECT id FROM active_storage_attachments WHERE blob_id=?", (blob["id"],)
        )
        == []
    )
    return blob


def direct_upload(app):
    assert app.login("kevin")[0] == 302
    content = b"0123456789"
    for form in (False, True):
        blob = allocate_blob(app, content, "audit.png", "image/png", form)
        file = (
            app.database.parent
            / "files"
            / blob["key"][:2]
            / blob["key"][2:4]
            / blob["key"]
        )
        assert not file.exists()
        status, _, headers, _ = app.fetch("public", "GET", blob["proxy_path"])
        assert status == 404 and "no-cache" in headers.get("Cache-Control", ""), (
            "unwritten download",
            status,
            dict(headers),
        )
        for body, mime_type in (
            (content, "text/plain"),
            (content[:-1], "image/png"),
            (b"abcdefghij", "image/png"),
        ):
            result = app.fetch(
                "kevin", "PUT", blob["upload_path"], body, {"Content-Type": mime_type}
            )
            assert result[0] == 422, ("invalid upload", result[0])
            assert not file.exists()
        assert (
            app.fetch(
                "kevin",
                "PUT",
                blob["upload_path"],
                content,
                blob["direct_upload"]["headers"],
            )[0]
            == 204
        )
        assert file.read_bytes() == content
        status, _, headers, body = app.fetch("public", "GET", blob["proxy_path"])
        assert (
            status == 200
            and body == content
            and "immutable" in headers.get("Cache-Control", "")
        )
    cookies = next(
        handler.cookiejar
        for handler in app.clients["kevin"].handlers
        if isinstance(handler, urllib.request.HTTPCookieProcessor)
    )
    retained = "; ".join(f"{cookie.name}={cookie.value}" for cookie in cookies)
    assert retained
    assert app.request("kevin", "DELETE", "/session", {})[0] == 302
    assert (
        app.fetch(
            "stale",
            "PUT",
            blob["upload_path"],
            content,
            {"Cookie": retained, "Content-Type": "image/png"},
        )[0]
        == 401
    )
    count = app.rows("SELECT count(*) FROM active_storage_blobs")
    assert (
        app.fetch(
            "stale",
            "POST",
            "/rails/active_storage/direct_uploads",
            b"{}",
            {"Cookie": retained, "Content-Type": "application/json"},
        )[0]
        == 401
    )
    assert (
        app.rows("SELECT count(*) FROM active_storage_blobs") == count
        and file.read_bytes() == content
    )
    assert app.fetch("public", "GET", blob["proxy_path"])[3] == content


def storage_downloads(app):
    assert app.login("kevin")[0] == 302
    content = b"0123456789"
    blob = allocate_blob(app, content, "audit.png", "image/png")
    assert (
        app.fetch(
            "kevin",
            "PUT",
            blob["upload_path"],
            content,
            blob["direct_upload"]["headers"],
        )[0]
        == 204
    )
    redirect = blob["proxy_path"].replace("/proxy/", "/redirect/")
    status, disk, _, _ = app.fetch("public", "GET", redirect)
    assert status == 302
    for path, proxy in (
        (disk, False),
        (blob["proxy_path"] + "?disposition=attachment", True),
    ):
        status, _, headers, body = app.fetch(
            "public", "GET", path, headers={"Range": "bytes=1-2"}
        )
        assert (
            status == 206
            and body == b"12"
            and headers["Content-Range"] == "bytes 1-2/10"
        )
        if proxy:
            assert headers["Content-Disposition"].startswith("inline;"), (
                "ranged disposition",
                headers["Content-Disposition"],
            )
        head = app.fetch("public", "HEAD", path, headers={"Range": "bytes=1-2"})
        assert (
            head[0] == 206
            and head[3] == b""
            and head[2]["Content-Range"] == headers["Content-Range"]
            and head[2]["Content-Length"] == "2"
        )
        status, _, headers, body = app.fetch(
            "public", "GET", path, headers={"Range": "bytes=99-"}
        )
        assert status == 416 and body == (
            b"" if proxy else b"Byte range unsatisfiable\n"
        )
        if not proxy:
            assert headers["Content-Range"] == "bytes */10"
        response = app.fetch("public", "GET", path)
        key, value = (
            ("If-None-Match", response[2]["ETag"])
            if proxy
            else ("If-Modified-Since", response[2]["Last-Modified"])
        )
        conditional = app.fetch("public", "GET", path, headers={key: value})
        assert conditional[0] == 304 and conditional[3] == b""
    room = app.labels["rooms.designers"]
    body = app.request("kevin", "GET", f"/rooms/{room}")[2]
    representation = re.search(
        r'(?:src|href)="([^"]*/rails/active_storage/representations/redirect/[^"]+)"',
        body,
    )
    assert representation, "missing seeded image representation"
    path = representation[1].removeprefix(app.base)
    path = path.replace("/redirect/", "/proxy/") + "?disposition=attachment"
    status, _, headers, full = app.fetch("public", "GET", path)
    assert (
        status == 200
        and full
        and headers["Content-Disposition"].startswith("attachment;")
    ), ("representation", status, dict(headers))
    status, _, ranged, data = app.fetch(
        "public", "GET", path, headers={"Range": "bytes=0-1"}
    )
    assert status == 200 and data == full and not ranged.get("Content-Range"), (
        "ranged representation",
        status,
        len(data),
        len(full),
    )
    response = app.fetch(
        "public",
        "GET",
        path,
        headers={"Range": "bytes=99-", "If-None-Match": headers["ETag"]},
    )
    assert response[0] == 304 and response[3] == b""


def multipart_upload(app, actor, method, path, field, fixture, filename, fields=None):
    content = (ROOT / "reference/reference/test/fixtures/files" / fixture).read_bytes()
    boundary = "CampfireContractBoundary"
    assert boundary.encode() not in content
    parts = []
    for key, value in (fields or {}).items():
        parts.append(
            f'--{boundary}\r\nContent-Disposition: form-data; name="{key}"\r\n\r\n{value}\r\n'.encode()
        )
    content_type = "image/jpeg" if fixture.endswith(".jpg") else "video/quicktime"
    parts.extend(
        [
            f'--{boundary}\r\nContent-Disposition: form-data; name="{field}"; filename="{filename}"\r\nContent-Type: {content_type}\r\n\r\n'.encode(),
            content,
            f"\r\n--{boundary}--\r\n".encode(),
        ]
    )
    result = app.fetch(
        actor,
        method,
        path,
        b"".join(parts),
        {
            "Content-Type": f"multipart/form-data; boundary={boundary}",
            "Accept": "text/vnd.turbo-stream.html, text/html"
            if method == "POST"
            else "text/html",
        },
    )
    return result, content


def file_path(app, key):
    return app.database.parent / "files" / key[:2] / key[2:4] / key


def blob_graph(app, source):
    graph, pending = {}, [source]
    while pending:
        blob = pending.pop()
        if blob in graph:
            continue
        rows = app.rows(
            "SELECT key,byte_size,checksum FROM active_storage_blobs WHERE id=?",
            (blob,),
        )
        assert len(rows) == 1, ("missing graph blob", blob, rows)
        graph[blob] = rows[0][0]
        content = file_path(app, rows[0][0]).read_bytes()
        assert (
            len(content) == rows[0][1]
            and base64.b64encode(hashlib.md5(content).digest()).decode() == rows[0][2]
        ), ("graph bytes", blob)
        pending.extend(
            row[0]
            for row in app.rows(
                "SELECT blob_id FROM active_storage_attachments WHERE "
                "(record_type='ActiveStorage::Blob' AND record_id=? AND name='preview_image') OR "
                "(record_type='ActiveStorage::VariantRecord' AND record_id IN "
                "(SELECT id FROM active_storage_variant_records WHERE blob_id=?))",
                (blob, blob),
            )
        )
    return graph


def eventually(condition, label):
    deadline = time.monotonic() + 10
    while not condition():
        assert time.monotonic() < deadline, ("background work did not complete", label)
        time.sleep(0.05)


def purged(app, graph):
    ids = tuple(graph)
    slots = ",".join("?" for _ in ids)
    eventually(
        lambda: (
            not app.rows(
                f"SELECT id FROM active_storage_blobs WHERE id IN ({slots})", ids
            )
            and not any(file_path(app, key).exists() for key in graph.values())
        ),
        "blob/file purge",
    )
    assert not app.rows(
        f"SELECT id FROM active_storage_attachments WHERE blob_id IN ({slots})", ids
    )
    assert not app.rows(
        f"SELECT id FROM active_storage_variant_records WHERE blob_id IN ({slots})", ids
    )


def attached_blob(app, kind, record, name):
    rows = app.rows(
        "SELECT b.id,b.filename,b.metadata FROM active_storage_blobs b JOIN active_storage_attachments a ON a.blob_id=b.id "
        "WHERE a.record_type=? AND a.record_id=? AND a.name=?",
        (kind, record, name),
    )
    assert len(rows) == 1, ("attachment cardinality", kind, record, name, rows)
    return rows[0]


def attachment_lifecycle(app):
    assert app.login("kevin")[0] == 302
    room = app.labels["rooms.designers"]
    route = f"/rooms/{room}/messages"
    before = app.rows("SELECT count(*) FROM active_storage_blobs")[0][0]
    files = {
        p.relative_to(app.database.parent / "files")
        for p in (app.database.parent / "files").rglob("*")
        if p.is_file()
    }
    client = "contract-attachment-client"
    result, content = multipart_upload(
        app,
        "kevin",
        "POST",
        route,
        "message[attachment]",
        "moon.jpg",
        "contractmoon.jpg",
        {"message[client_message_id]": client},
    )
    assert result[0] == 200 and b'action="append"' in result[3], (
        "attachment create",
        result[0],
    )
    rows = app.rows(
        "SELECT id,creator_id FROM messages WHERE client_message_id=?", (client,)
    )
    assert len(rows) == 1 and rows[0][1] == app.labels["users.kevin"]
    message = rows[0][0]
    path = route + f"/{message}"
    no_body = lambda: (
        not app.rows(
            "SELECT id FROM action_text_rich_texts WHERE record_type='Message' AND record_id=?",
            (message,),
        )
    )
    assert no_body(), "omitted body created rich text"
    blob, filename, metadata = attached_blob(app, "Message", message, "attachment")
    assert filename == "contractmoon.jpg" and json.loads(metadata)["analyzed"]
    expected = next(
        x
        for x in json.loads((ROOT / "reference/vectors/storage.json").read_text())[
            "messages"
        ]
        if x["fixture"] == "moon.jpg"
    )
    dimensions = json.loads(expected["blob"]["metadata"])
    assert all(
        json.loads(metadata)[key] == dimensions[key] for key in ("width", "height")
    )
    graph = blob_graph(app, blob)
    assert len(graph) >= 2 and file_path(app, graph[blob]).read_bytes() == content
    assert app.rows(
        "SELECT rowid FROM message_search_index WHERE message_search_index MATCH 'contractmoon'"
    ) == [(message,)]
    # Warm the room's message and filename search before replacing the attachment.
    assert "contractmoon.jpg" in app.request("kevin", "GET", path)[2]
    assert (
        f'data-message-id="{message}"'
        in app.request("kevin", "GET", "/searches?q=contractmoon")[2]
    )
    result, content = multipart_upload(
        app,
        "kevin",
        "PATCH",
        path,
        "message[attachment]",
        "alpha-centuri.mov",
        "contractvideo.mov",
    )
    assert result[:2] == (302, path), ("attachment replacement", result[:2])
    purged(app, graph)
    blob, filename, _ = attached_blob(app, "Message", message, "attachment")
    assert filename == "contractvideo.mov" and no_body()
    eventually(
        lambda: json.loads(attached_blob(app, "Message", message, "attachment")[2]).get(
            "analyzed"
        ),
        "video analysis",
    )
    metadata = json.loads(attached_blob(app, "Message", message, "attachment")[2])
    assert metadata["video"] and metadata["width"] == 320 and metadata["height"] == 180
    page = app.request("kevin", "GET", path)
    assert (
        page[0] == 200
        and "contractvideo.mov" in page[2]
        and "contractmoon.jpg" not in page[2]
    )
    representation = re.search(
        r'(?:src|href|poster)="([^"]*/rails/active_storage/representations/redirect/[^"]+)"',
        page[2],
    )
    assert representation, "video presentation lacks preview URL"
    preview = (
        html.unescape(representation[1])
        .removeprefix(app.base)
        .replace("/redirect/", "/proxy/")
    )
    response = app.fetch("public", "GET", preview)
    assert (
        response[0] == 200
        and response[3]
        and response[2]["Content-Type"].startswith("image/")
    )
    graph = blob_graph(app, blob)
    assert len(graph) >= 3 and file_path(app, graph[blob]).read_bytes() == content
    assert app.rows(
        "SELECT rowid FROM message_search_index WHERE message_search_index MATCH 'contractvideo'"
    ) == [(message,)]
    assert not app.rows(
        "SELECT rowid FROM message_search_index WHERE message_search_index MATCH 'contractmoon'"
    )
    assert app.request("kevin", "PATCH", path, {"message[attachment]": ""})[:2] == (
        302,
        path,
    )
    purged(app, graph)
    assert not app.rows(
        "SELECT id FROM active_storage_attachments WHERE record_type='Message' AND record_id=?",
        (message,),
    )
    assert no_body() and app.rows(
        "SELECT body FROM message_search_index WHERE rowid=?", (message,)
    ) == [("",)]
    assert "contractvideo.mov" not in app.request("kevin", "GET", path)[2]
    assert app.rows("SELECT count(*) FROM active_storage_blobs")[0][0] == before
    assert {
        p.relative_to(app.database.parent / "files")
        for p in (app.database.parent / "files").rglob("*")
        if p.is_file()
    } == files
    assert app.request("kevin", "DELETE", path, {})[0] == 200
    assert not app.rows("SELECT id FROM messages WHERE id=?", (message,))
    assert not app.rows(
        "SELECT rowid FROM message_search_index WHERE rowid=?", (message,)
    )


def avatar_lifecycle(app):
    assert app.login("jason")[0] == 302
    user = app.labels["users.jason"]
    path = "/users/" + app.labels["avatar_tokens.jason"] + "/avatar"
    original = attached_blob(app, "User", user, "avatar")
    response = app.fetch("jason", "GET", path)
    assert (
        response[0] == 200
        and response[3]
        and response[2]["Content-Type"].startswith("image/webp")
    )
    assert response[2].get("ETag"), "avatar lacks record validator"
    assert (
        app.fetch("jason", "GET", path, headers={"If-None-Match": response[2]["ETag"]})[
            0
        ]
        == 304
    )
    graph = blob_graph(app, original[0])

    def profile(avatar, bio):
        return app.fetch(
            "jason",
            "PATCH",
            "/users/me/profile",
            json.dumps({"user": {"avatar": avatar, "bio": bio}}).encode(),
            {"Content-Type": "application/json"},
        )

    assert profile(None, "contract-avatar-kept")[0] == 302
    assert attached_blob(app, "User", user, "avatar")[0] == original[0]
    assert profile("invalid", "contract-avatar-rollback")[0] == 500
    assert app.rows("SELECT bio FROM users WHERE id=?", (user,)) == [
        ("contract-avatar-kept",)
    ]
    assert attached_blob(app, "User", user, "avatar")[0] == original[0]
    # Assign a real multipart avatar, replacing and purging the old owned graph.
    result, _ = multipart_upload(
        app,
        "jason",
        "PATCH",
        "/users/me/profile",
        "user[avatar]",
        "black_hole.jpg",
        "contractavatar.jpg",
    )
    assert result[0] == 302
    purged(app, graph)
    new = attached_blob(app, "User", user, "avatar")
    assert new[0] != original[0] and new[1] == "contractavatar.jpg"
    eventually(
        lambda: json.loads(attached_blob(app, "User", user, "avatar")[2]).get(
            "analyzed"
        ),
        "avatar analysis",
    )
    fresh = app.fetch(
        "jason", "GET", path, headers={"If-None-Match": response[2]["ETag"]}
    )
    assert fresh[0] == 200 and fresh[2]["ETag"] != response[2]["ETag"]
    graph = blob_graph(app, new[0])
    assert len(graph) >= 2
    assert profile("", "contract-avatar-kept")[0] == 302
    purged(app, graph)
    assert not app.rows(
        "SELECT id FROM active_storage_attachments WHERE record_type='User' AND record_id=? AND name='avatar'",
        (user,),
    )
    initials = app.fetch(
        "jason", "GET", path, headers={"If-None-Match": fresh[2]["ETag"]}
    )
    assert initials[0] == 200 and re.search(
        rb"<text\b[^>]*>\s*J\s*</text>", initials[3]
    ), ("detached avatar initials", initials[0], initials[3])
    assert initials[2]["ETag"] != fresh[2]["ETag"]
    head = app.fetch("jason", "HEAD", path)
    assert (
        head[0] == 200
        and not head[3]
        and head[2]["Content-Type"].startswith("image/svg+xml")
    )
    cookies = next(
        handler.cookiejar
        for handler in app.clients["jason"].handlers
        if isinstance(handler, urllib.request.HTTPCookieProcessor)
    )
    retained = "; ".join(f"{cookie.name}={cookie.value}" for cookie in cookies)
    assert app.request("jason", "DELETE", "/session", {})[0] == 302
    stale = app.fetch(
        "stale",
        "GET",
        path,
        headers={"Cookie": retained, "If-None-Match": initials[2]["ETag"]},
    )
    assert stale[0] == 302 and stale[1] == "/session/new", (
        "stale avatar session",
        stale[:2],
    )


def upload_metadata(app):
    assert app.login("kevin")[0] == 302
    fields = "blob[filename]=metadata.txt&blob[byte_size]=0&blob[checksum]=1B2M2Y8AsgTpgAmY7PhCfg%3D%3D"
    metadata = (
        "&blob[metadata][note]=caf%C3%A9+%26+tea"
        "&blob[metadata][nested][count]=42"
        "&blob[metadata][tags][]=a&blob[metadata][tags][]=b"
        "&blob[metadata][items][][name]=one&blob[metadata][items][][details][x]=1"
        "&blob[metadata][items][][details][y]=2&blob[metadata][items][][name]=two"
        "&blob[metadata][items][][tags][]=a&blob[metadata][items][][tags][]=b"
        "&blob[metadata][empty]=&blob[metadata][nil]"
        "&blob[metadata][numbered][0]=zero"
        "&blob[metadata][duplicate]=first&blob[metadata][duplicate]=last"
    )
    expected = {
        "note": "café & tea",
        "nested": {"count": "42"},
        "tags": ["a", "b"],
        "items": [
            {"name": "one", "details": {"x": "1", "y": "2"}},
            {"name": "two", "tags": ["a", "b"]},
        ],
        "empty": "",
        "nil": None,
        "numbered": {"0": "zero"},
        "duplicate": "last",
    }
    typed = {"flag": True, "count": 42, "nested": {"tags": ["x", False, None, 7]}}
    typed_expected = {"flag": True, "count": 42, "nested": {"tags": ["x", False, 7]}}
    cases = [
        ("application/x-www-form-urlencoded", fields + metadata, "", expected),
        (
            "application/x-www-form-urlencoded",
            fields + metadata,
            "?" + fields + "&blob[metadata][query]=only",
            {"query": "only"},
        ),
        (
            "application/x-www-form-urlencoded",
            fields + "&blob[metadata][]=ignored",
            "",
            {},
        ),
        (
            "application/json",
            json.dumps(
                {
                    "blob": {
                        "filename": "metadata.txt",
                        "byte_size": 0,
                        "checksum": "1B2M2Y8AsgTpgAmY7PhCfg==",
                        "metadata": typed,
                    }
                }
            ),
            "",
            typed_expected,
        ),
        (
            "application/json",
            json.dumps(
                {
                    "blob": {
                        "filename": "metadata.txt",
                        "byte_size": 0,
                        "checksum": "1B2M2Y8AsgTpgAmY7PhCfg==",
                        "metadata": typed,
                    }
                }
            ),
            "?" + fields + "&blob[metadata][query]=only",
            {"query": "only"},
        ),
    ]
    for content_type, body, query, want in cases:
        response = app.fetch(
            "kevin",
            "POST",
            "/rails/active_storage/direct_uploads" + query,
            body.encode(),
            {"Content-Type": content_type, "Accept": "application/json"},
        )
        assert response[0] == 200, ("metadata allocation", response[0], response[3])
        blob = json.loads(response[3])
        assert blob["metadata"] == want, ("response metadata", blob["metadata"], want)
        stored = app.rows(
            "SELECT metadata FROM active_storage_blobs WHERE id=?", (blob["id"],)
        )
        assert len(stored) == 1 and json.loads(stored[0][0]) == want, (
            "stored metadata",
            stored,
        )
        assert not file_path(app, blob["key"]).exists()
    before = app.rows("SELECT count(*) FROM active_storage_blobs")
    for extra in (
        "&blob[metadata][x]=scalar&blob[metadata][x][y]=nested",
        "&blob[metadata]" + "[x]" * 99 + "=deep",
    ):
        response = app.fetch(
            "kevin",
            "POST",
            "/rails/active_storage/direct_uploads",
            (fields + extra).encode(),
            {"Content-Type": "application/x-www-form-urlencoded"},
        )
        assert response[0] == 400, ("invalid form structure", response[0])
        assert app.rows("SELECT count(*) FROM active_storage_blobs") == before
