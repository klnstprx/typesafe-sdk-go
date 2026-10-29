"""Generate Python SDK parity expectations for the Go SDK.

Runs every case below through the official Python SDK, pinned to v0.7.5, and
writes the inputs and observed results to python-0.7.5.json. The Go test
parity_test.go replays the same inputs and compares; intentional differences
live in differences.json.

Usage (from the Go repository root):

    testdata/parity/regenerate.sh [path-to-typesafe-sdk-python]

The cases use only public behavior plus the internal parse_retry_after
function, which is stable for the pinned version.
"""

import base64
import contextlib
import json
import logging
import math
import os
import re
import subprocess
import sys
from pathlib import Path

import httpx2

import typesafe_sdk
from typesafe_sdk import Box, Choice, Image, Noul, RetryPolicy, Score, TypeSafeClient, TypeSafeError
from typesafe_sdk._core.errors import parse_retry_after

PINNED_VERSION = "0.7.5"
PINNED_COMMIT = "f36e68549ceca3191e5791e5f99ea1baa1a258b5"
API_KEY = "sk-parity-0123456789"
BASE_URL = "https://api.test"
OUTPUT = Path(__file__).with_name(f"python-{PINNED_VERSION}.json")

# Headers whose values are language- or HTTP-library specific.
IGNORED_REQUEST_HEADERS = {"user-agent", "x-typesafe-sdk", "x-typesafe-runtime", "accept-encoding", "connection", "host", "content-length"}

OK_BODY = '{"model":"jev-1","usage":{"input_tokens":3,"output_tokens":1},"answers":{"q":{"type":"noul","noul":0.5}}}'
USAGE = '"usage":{"input_tokens":1,"output_tokens":2}'


def sysone(answers: str) -> str:
    return '{"model":"m",' + USAGE + ',"answers":{' + answers + "}}"


# ---------------------------------------------------------------------------
# Cases. Each response case is sent to /v1/systemone (or /v1/models) with
# retries disabled, and the canned reply is returned.

RESPONSES = [
    # Successful bodies.
    ("full", 200, {"x-typesafe-request-id": "req-1"}, """{
      "model": "jev-1", "usage": {"input_tokens": 120, "output_tokens": 12, "cached": 3},
      "answers": {
        "spam": {"type": "noul", "noul": 0.98, "extra": true},
        "tone": {"type": "choice", "choice": "calm", "confidence": 0.9, "probabilities": {"calm": 0.9, "angry": 0.1}},
        "urgency": {"type": "score", "score": 1.7, "confidence": 0.8,
                    "legend": {"0": "low", "1": {"level": "mid"}, "2": ["high", null]},
                    "probabilities": {"0": 0.1, "1": 0.1, "2": 0.8}}
      },
      "trace": "ignored"}"""),
    ("no request id", 200, {}, OK_BODY),
    ("status 201", 201, {}, OK_BODY),
    ("integers for floats", 200, {}, sysone('"n":{"type":"noul","noul":1},"c":{"type":"choice","choice":"a","confidence":1,"probabilities":{"a":1}}')),
    ("zero values", 200, {}, sysone('"n":{"type":"noul","noul":0},"s":{"type":"score","score":0,"confidence":0,"legend":{},"probabilities":{}}')),
    ("negative legend key", 200, {}, sysone('"s":{"type":"score","score":1,"confidence":1,"legend":{"-1":"x"},"probabilities":{"-1":1}}')),
    ("answers absent", 200, {}, '{"model":"m",' + USAGE + "}"),
    ("answers empty", 200, {}, sysone("")),
    ("usage empty", 200, {}, '{"model":"m","usage":{},"answers":{}}'),
    ("usage nulls", 200, {}, '{"model":"m","usage":{"input_tokens":null,"output_tokens":null},"answers":{}}'),
    ("unknown type only", 200, {}, sysone('"r":{"type":"ranking","order":[1,2]}')),
    ("unknown type mixed", 200, {}, sysone('"r":{"type":"ranking"},"n":{"type":"noul","noul":0.1},"x":{"type":"essay"}')),
    ("unknown type skips validation", 200, {}, sysone('"r":{"type":"ranking","noul":"not a number"}')),
    # Validation paths.
    ("body array", 200, {}, "[1]"),
    ("body not json", 200, {}, "oops"),
    ("body empty", 200, {}, ""),
    ("body null", 200, {}, "null"),
    ("status 204 empty", 204, {}, ""),
    ("model missing", 200, {"x-typesafe-request-id": "req-v"}, "{" + USAGE + "}"),
    ("model number", 200, {}, '{"model":1,' + USAGE + "}"),
    ("model null", 200, {}, '{"model":null,' + USAGE + "}"),
    ("usage missing", 200, {}, '{"model":"m"}'),
    ("usage null", 200, {}, '{"model":"m","usage":null}'),
    ("usage array", 200, {}, '{"model":"m","usage":[]}'),
    ("usage token string", 200, {}, '{"model":"m","usage":{"input_tokens":"1"}}'),
    ("usage token bool", 200, {}, '{"model":"m","usage":{"input_tokens":true}}'),
    ("usage token fraction", 200, {}, '{"model":"m","usage":{"output_tokens":1.5}}'),
    ("usage token integral float", 200, {}, '{"model":"m","usage":{"output_tokens":1.0}}'),
    ("usage token beyond int64", 200, {}, '{"model":"m","usage":{"input_tokens":100000000000000000000},"answers":{}}'),
    ("answers null", 200, {}, '{"model":"m",' + USAGE + ',"answers":null}'),
    ("answers array", 200, {}, '{"model":"m",' + USAGE + ',"answers":[]}'),
    ("answer not object", 200, {}, sysone('"c":"x"')),
    ("answer null", 200, {}, sysone('"c":null')),
    ("answer type missing", 200, {}, sysone('"c":{}')),
    ("answer type number", 200, {}, sysone('"c":{"type":1}')),
    ("answer type checked before model", 200, {}, '{"answers":{"c":{"type":1}}}'),
    ("noul missing", 200, {}, sysone('"n":{"type":"noul"}')),
    ("noul string", 200, {}, sysone('"n":{"type":"noul","noul":"0.5"}')),
    ("noul null", 200, {}, sysone('"n":{"type":"noul","noul":null}')),
    ("noul bool", 200, {}, sysone('"n":{"type":"noul","noul":false}')),
    ("noul NaN literal", 200, {}, sysone('"n":{"type":"noul","noul":NaN}')),
    ("choice missing choice", 200, {}, sysone('"c":{"type":"choice","confidence":1,"probabilities":{}}')),
    ("choice choice number", 200, {}, sysone('"c":{"type":"choice","choice":1,"confidence":1,"probabilities":{}}')),
    ("choice confidence bool", 200, {}, sysone('"c":{"type":"choice","choice":"a","confidence":true,"probabilities":{}}')),
    ("choice probabilities missing", 200, {}, sysone('"c":{"type":"choice","choice":"a","confidence":1}')),
    ("choice probabilities array", 200, {}, sysone('"c":{"type":"choice","choice":"a","confidence":1,"probabilities":[]}')),
    ("choice probability null", 200, {}, sysone('"c":{"type":"choice","choice":"a","confidence":1,"probabilities":{"a":1,"x":null}}')),
    ("choice probability string", 200, {}, sysone('"c":{"type":"choice","choice":"a","confidence":1,"probabilities":{"x":"1"}}')),
    ("choice probability document order", 200, {}, sysone('"c":{"type":"choice","choice":"a","confidence":1,"probabilities":{"z":null,"a":null}}')),
    ("score missing score", 200, {}, sysone('"s":{"type":"score","confidence":1,"legend":{},"probabilities":{}}')),
    ("score legend array", 200, {}, sysone('"s":{"type":"score","score":1,"confidence":1,"legend":[],"probabilities":{}}')),
    ("score legend key", 200, {}, sysone('"s":{"type":"score","score":1,"confidence":1,"legend":{"x":"a"},"probabilities":{}}')),
    ("score legend number", 200, {}, sysone('"s":{"type":"score","score":1,"confidence":1,"legend":{"0":1},"probabilities":{}}')),
    ("score legend bool", 200, {}, sysone('"s":{"type":"score","score":1,"confidence":1,"legend":{"0":true},"probabilities":{}}')),
    ("score legend null", 200, {}, sysone('"s":{"type":"score","score":1,"confidence":1,"legend":{"0":null},"probabilities":{}}')),
    ("score probability key", 200, {}, sysone('"s":{"type":"score","score":1,"confidence":1,"legend":{},"probabilities":{"1.5":1}}')),
    ("score probability null", 200, {}, sysone('"s":{"type":"score","score":1,"confidence":1,"legend":{},"probabilities":{"0":null}}')),
    ("first invalid answer in document order", 200, {}, sysone('"z":{"type":"noul"},"a":{"type":"noul"}')),
    ("box ok", 200, {}, sysone('"b":{"type":"box","box":{"top":0.2,"bottom":0.66,"left":0.19,"right":0.68},"confidence":0.9}')),
    ("box confidence null", 200, {}, sysone('"b":{"type":"box","box":{"top":0.2,"bottom":0.66,"left":0.19,"right":0.68},"confidence":null}')),
    ("box integer coordinates", 200, {}, sysone('"b":{"type":"box","box":{"top":0,"bottom":1,"left":0,"right":1},"confidence":1}')),
    ("box extra fields ignored", 200, {}, sysone('"b":{"type":"box","box":{"top":0,"bottom":1,"left":0,"right":1,"z":"x"},"confidence":null,"extra":1}')),
    ("box confidence missing", 200, {}, sysone('"b":{"type":"box","box":{"top":0,"bottom":1,"left":0,"right":1}}')),
    ("box confidence string", 200, {}, sysone('"b":{"type":"box","box":{"top":0,"bottom":1,"left":0,"right":1},"confidence":"0.9"}')),
    ("box confidence bool", 200, {}, sysone('"b":{"type":"box","box":{"top":0,"bottom":1,"left":0,"right":1},"confidence":true}')),
    ("box box missing", 200, {}, sysone('"b":{"type":"box","confidence":null}')),
    ("box box null", 200, {}, sysone('"b":{"type":"box","box":null,"confidence":null}')),
    ("box box array", 200, {}, sysone('"b":{"type":"box","box":[0,1,0,1],"confidence":null}')),
    ("box coordinate missing", 200, {}, sysone('"b":{"type":"box","box":{"top":0,"bottom":1,"right":1},"confidence":null}')),
    ("box coordinate string", 200, {}, sysone('"b":{"type":"box","box":{"top":0,"bottom":"1","left":0,"right":1},"confidence":null}')),
    ("box coordinate null", 200, {}, sysone('"b":{"type":"box","box":{"top":0,"bottom":1,"left":null,"right":1},"confidence":null}')),
    ("box coordinate bool", 200, {}, sysone('"b":{"type":"box","box":{"top":false,"bottom":1,"left":0,"right":1},"confidence":null}')),
    ("box coordinates checked in field order", 200, {}, sysone('"b":{"type":"box","box":{"right":"x","top":"x"},"confidence":"x"}')),
    ("box before confidence", 200, {}, sysone('"b":{"type":"box","confidence":"x","box":null}')),
    ("box mixed with others", 200, {}, sysone('"n":{"type":"noul","noul":0.5},"b":{"type":"box","box":{"top":0,"bottom":1,"left":0,"right":1},"confidence":null}')),
    # API errors and message extraction.
    ("error string", 400, {"x-typesafe-request-id": "req-e"}, '{"error":"bad","message":"m"}'),
    ("error.message", 401, {}, '{"error":{"message":"nested"},"message":"m"}'),
    ("message", 403, {}, '{"message":"msg","detail":"d"}'),
    ("detail string", 404, {}, '{"detail":"d"}'),
    ("detail.message", 422, {}, '{"detail":{"message":"dm"}}'),
    ("detail list", 422, {}, '{"detail":[{"loc":["body","questions","q","score","criteria",0],"msg":"Invalid"},{"loc":["body"],"msg":"Missing"},"junk",{"loc":"x","msg":"m"},{"msg":5}]}'),
    ("detail list empty", 422, {}, '{"detail":[]}'),
    ("rate limited", 429, {"x-typesafe-request-id": "req-1", "retry-after": "7"}, '{"error":"Rate limit exceeded"}'),
    ("empty error string dumps json", 400, {}, '{"error":""}'),
    ("compact json dump", 500, {}, '{ "code" : 7 }'),
    ("json fallback truncated", 503, {}, '{"data":"' + "x" * 250 + '"}'),
    ("plain text not truncated", 502, {}, "y" * 250),
    ("empty body", 500, {}, ""),
    ("json null body", 500, {}, "null"),
    ("empty array body", 500, {}, "[]"),
    ("number body", 500, {}, "42"),
    ("unicode message", 400, {}, '{"error":"caf\\u00e9 ☕"}'),
    ("redirect", 302, {"location": "/elsewhere"}, ""),
    ("teapot", 418, {}, '{"error":"short and stout"}'),
]

# Bodies that are not valid UTF-8, given as base64.
RESPONSES_B64 = [
    ("invalid utf-8 text", 400, {}, base64.b64encode(b"bad \xff byte").decode()),
]

MODELS = [
    ("models ok", 200, {"x-typesafe-request-id": "req-m"}, '{"models":[{"name":"jev-latest","description":"General.","release_date":"2026-09-15","extra":1}]}'),
    ("models empty", 200, {}, '{"models":[]}'),
    ("models body null", 200, {}, "null"),
    ("models missing", 200, {}, "{}"),
    ("models not array", 200, {}, '{"models":"bad"}'),
    ("models card field missing", 200, {}, '{"models":[{"name":"a","description":"d","release_date":"r"},{"description":"d","release_date":"r"}]}'),
    ("models card not object", 200, {}, '{"models":[1]}'),
    ("models date number", 200, {}, '{"models":[{"name":"a","description":"d","release_date":5}]}'),
    ("models error", 401, {}, '{"error":"invalid key"}'),
]

# A valid question for cases that exercise responses and retries rather than encoding.
PROBE = {"q": Noul(instructions="?")}

# Request cases: questions use {"kind": ..., fields} for typed questions and
# {"raw": {...}} for dictionaries.
NOUL = {"kind": "noul", "instructions": "Is this spam?"}
REQUESTS = [
    {"name": "text state", "state": "hello", "questions": {"q": NOUL}},
    {"name": "object state", "state": {"subject": "dup", "tags": [None, 1, "a"], "nested": {"x": None}}, "questions": {"q": NOUL}},
    {"name": "array state", "state": ["a", {"b": 1}], "questions": {"q": NOUL}},
    {"name": "unicode state", "state": "café ☕ <b>&", "questions": {"q": NOUL}},
    {"name": "number state", "state": 5, "questions": {"q": NOUL}},
    {"name": "null state", "state": None, "questions": {"q": NOUL}},
    {"name": "bool state", "state": True, "questions": {"q": NOUL}},
    {"name": "noul empty", "state": "x", "questions": {"q": {"kind": "noul"}}},
    {"name": "noul empty instructions", "state": "x", "questions": {"q": {"kind": "noul", "instructions": ""}}},
    {"name": "noul structured", "state": "x", "questions": {"q": {"kind": "noul", "instructions": {"task": "t", "n": None}, "criteria": {"true": "yes", "false": ["no"]}}}},
    {"name": "noul criteria one side", "state": "x", "questions": {"q": {"kind": "noul", "criteria": {"true": "yes"}}}},
    {"name": "noul criteria explicit null", "state": "x", "questions": {"q": {"kind": "noul", "criteria": {"true": None, "false": "no"}}}},
    {"name": "noul criteria all null", "state": "x", "questions": {"q": {"kind": "noul", "criteria": {"true": None, "false": None}}}},
    {"name": "noul empty criteria", "state": "x", "questions": {"q": {"kind": "noul", "criteria": {}}}},
    {"name": "noul number instructions", "state": "x", "questions": {"q": {"kind": "noul", "instructions": 5}}},
    {"name": "choice", "state": "x", "questions": {"q": {"kind": "choice", "instructions": "Tone?", "criteria": {"calm": None, "angry": "upset", "x": {"a": 1}}}}},
    {"name": "choice empty criteria", "state": "x", "questions": {"q": {"kind": "choice", "criteria": {}}}},
    {"name": "score", "state": "x", "questions": {"q": {"kind": "score", "instructions": "Urgency?", "criteria": ["low", {"level": "mid"}, ["high"]]}}},
    {"name": "score empty criteria", "state": "x", "questions": {"urgency": {"kind": "score", "criteria": []}}},
    {"name": "mixed questions", "state": "x", "questions": {"b": NOUL, "a": {"kind": "score", "criteria": ["x"]}, "c": {"raw": {"type": "noul", "instructions": "?"}}}},
    {"name": "no questions", "state": "x", "questions": {}},
    {"name": "raw verbatim", "state": "x", "questions": {"q": {"raw": {"type": "future", "extra": [None], "instructions": 5}}}},
    {"name": "box empty", "state": "x", "questions": {"q": {"kind": "box"}}},
    {"name": "box instructions", "state": "x", "questions": {"q": {"kind": "box", "instructions": "The dog"}}},
    {"name": "box structured instructions", "state": "x", "questions": {"q": {"kind": "box", "instructions": {"find": ["dog", None]}}}},
    {"name": "box empty instructions", "state": "x", "questions": {"q": {"kind": "box", "instructions": ""}}},
    {"name": "box number instructions", "state": "x", "questions": {"q": {"kind": "box", "instructions": 5}}},
    {"name": "raw box", "state": "x", "questions": {"q": {"raw": {"type": "box", "instructions": "The dog"}}}},
    {"name": "raw box empty", "state": "x", "questions": {"q": {"raw": {"type": "box"}}}},
    {"name": "raw noul", "state": "x", "questions": {"q": {"raw": {"type": "noul", "instructions": "?"}}}},
    {"name": "raw noul empty", "state": "x", "questions": {"q": {"raw": {"type": "noul"}}}},
    {"name": "raw noul null instructions", "state": "x", "questions": {"q": {"raw": {"type": "noul", "instructions": None, "criteria": {"true": None}}}}},
    {"name": "raw noul criteria only", "state": "x", "questions": {"q": {"raw": {"type": "noul", "criteria": {"false": "no"}}}}},
    {"name": "raw noul string criteria", "state": "x", "questions": {"q": {"raw": {"type": "noul", "instructions": "?", "criteria": "yes"}}}},
    {"name": "raw noul array criteria", "state": "x", "questions": {"q": {"raw": {"type": "noul", "instructions": "?", "criteria": ["yes"]}}}},
    {"name": "raw noul null criteria", "state": "x", "questions": {"q": {"raw": {"type": "noul", "instructions": "?", "criteria": None}}}},
    {"name": "raw no type", "state": "x", "questions": {"x": {"raw": {"instructions": "?"}}}},
    {"name": "raw empty type", "state": "x", "questions": {"x": {"raw": {"type": ""}}}},
    {"name": "raw number type", "state": "x", "questions": {"x": {"raw": {"type": 1}}}},
    {"name": "raw choice no criteria", "state": "x", "questions": {"x": {"raw": {"type": "choice"}}}},
    {"name": "raw score no criteria", "state": "x", "questions": {"x": {"raw": {"type": "score"}}}},
    {"name": "raw score empty criteria", "state": "x", "questions": {"u": {"raw": {"type": "score", "criteria": []}}}},
    {"name": "raw score false criteria", "state": "x", "questions": {"u": {"raw": {"type": "score", "criteria": False}}}},
    {"name": "raw score zero criteria", "state": "x", "questions": {"u": {"raw": {"type": "score", "criteria": 0}}}},
    {"name": "raw score float zero criteria", "state": "x", "questions": {"u": {"raw": {"type": "score", "criteria": 0.0}}}},
    {"name": "raw score empty string criteria", "state": "x", "questions": {"u": {"raw": {"type": "score", "criteria": ""}}}},
    {"name": "raw score empty object criteria", "state": "x", "questions": {"u": {"raw": {"type": "score", "criteria": {}}}}},
    {"name": "raw score null criteria", "state": "x", "questions": {"u": {"raw": {"type": "score", "criteria": None}}}},
    {"name": "raw score truthy criteria", "state": "x", "questions": {"u": {"raw": {"type": "score", "criteria": 1}}}},
    {"name": "raw choice empty criteria", "state": "x", "questions": {"u": {"raw": {"type": "choice", "criteria": {}}}}},
    {"name": "raw choice truthy criteria", "state": "x", "questions": {"u": {"raw": {"type": "choice", "criteria": "x"}}}},
    {"name": "raw choice null criteria", "state": "x", "questions": {"u": {"raw": {"type": "choice", "criteria": None}}}},
    {"name": "non-finite state", "state": {"nan": math.nan, "inf": math.inf, "ninf": -math.inf}, "questions": {"q": NOUL}},
    {"name": "non-finite instructions", "state": "x", "questions": {"q": {"kind": "noul", "instructions": {"n": math.nan}}}},
    {"name": "non-finite extra body", "state": "x", "questions": {"q": NOUL}, "extra_body": {"temperature": math.inf}},
    {"name": "model override", "state": "x", "questions": {"q": NOUL}, "model": "jev-2"},
    {"name": "client model", "state": "x", "questions": {"q": NOUL}, "client_model": "jev-client"},
    {"name": "extra body", "state": "x", "questions": {"q": NOUL}, "model": "jev-2", "extra_body": {"model": "jev-extra", "trace": None, "debug": True}},
    {"name": "extra body replaces state", "state": "x", "questions": {"q": NOUL}, "extra_body": {"state": {"a": 1}}},
    {"name": "headers", "state": "x", "questions": {"q": NOUL},
     "client_headers": {"X-Team": "a", "X-Shared": "client", "Authorization": "Bearer stolen", "X-TypeSafe-Retry-Count": "99"},
     "headers": {"x-shared": "call", "Accept": "text/html", "Content-Type": "text/plain", "x-typesafe-retry-count": "7"}},
]

# Image cases. Images come from IMAGES by index: {"$image": i} places image i
# in content, {"$text": "... {i} ..."} formats its reference into text, and
# "attach" lists images attached to the call. Each case gets fresh images.
IMAGES = [b"\x89PNG\r\n\x1a\npng-0", b"\xff\xd8\xffjpeg-1", b"GIF89a-2"]
IMAGE_REQUESTS = [
    {"name": "image state", "state": {"$image": 0}, "questions": {"q": NOUL}},
    {"name": "image in object state", "state": {"photo": {"$image": 0}, "note": "x"}, "questions": {"q": NOUL}},
    {"name": "image in instructions", "state": "x", "questions": {"q": {"kind": "noul", "instructions": {"$image": 0}}}},
    {"name": "image in noul criteria", "state": "x", "questions": {"q": {"kind": "noul", "instructions": "?", "criteria": {"true": {"$image": 0}}}}},
    {"name": "image in choice criteria", "state": "x", "questions": {"q": {"kind": "choice", "criteria": {"a": {"$image": 0}, "b": None}}}},
    {"name": "image in score criteria", "state": "x", "questions": {"q": {"kind": "score", "criteria": [{"$image": 0}, "low"]}}},
    {"name": "image in raw question", "state": "x", "questions": {"q": {"raw": {"type": "noul", "instructions": {"$image": 0}}}}},
    {"name": "box on image", "state": {"$image": 1}, "questions": {"q": {"kind": "box", "instructions": {"$image": 0}}}},
    {"name": "same image twice", "state": [{"$image": 0}, {"$image": 0}], "questions": {"q": NOUL}},
    {"name": "images in order of appearance", "state": [{"$image": 1}, {"$image": 0}], "questions": {"q": NOUL}},
    {"name": "image content types", "state": [{"$image": 0}, {"$image": 1}, {"$image": 2}], "questions": {"q": NOUL}},
    {"name": "image in extra body", "state": "x", "questions": {"q": NOUL}, "extra_body": {"context": {"$image": 0}}},
    {"name": "text reference attached", "state": {"$text": "Compare {0} with the listing."}, "questions": {"q": NOUL}, "attach": [0]},
    {"name": "text reference in instructions attached", "state": "x", "questions": {"q": {"kind": "noul", "instructions": {"$text": "Is {0} damaged?"}}}, "attach": [0]},
    {"name": "text reference also placed", "state": {"a": {"$image": 0}, "b": {"$text": "see {0}"}}, "questions": {"q": NOUL}},
    {"name": "text reference not attached", "state": {"$text": "Compare {0}."}, "questions": {"q": NOUL}},
    {"name": "lookalike reference", "state": "see <image:0123456789abcdef0123456789abcdef>", "questions": {"q": NOUL}},
    {"name": "attached but unreferenced", "state": "x", "questions": {"q": NOUL}, "attach": [0]},
    {"name": "extra body assets override", "state": {"$image": 0}, "questions": {"q": NOUL},
     "extra_body": {"assets": {"p": {"type": "image", "format": "png", "data_base64": "AA=="}}}},
]

# Image constructors: "bytes" values are base64 of the file bytes, "base64"
# values are passed to Image.from_base64 as is. Each image is sent as state.
IMAGE_INPUTS = [
    {"name": "bytes", "kind": "bytes", "value": base64.b64encode(IMAGES[0]).decode()},
    {"name": "bytes empty", "kind": "bytes", "value": ""},
    {"name": "base64", "kind": "base64", "value": base64.b64encode(IMAGES[1]).decode()},
    {"name": "base64 wrapped", "kind": "base64", "value": " " + "\r\n".join(re.findall(".{1,4}", base64.b64encode(IMAGES[2]).decode())) + "\t\n"},
    {"name": "base64 empty", "kind": "base64", "value": ""},
    {"name": "base64 whitespace", "kind": "base64", "value": " \n\t"},
    {"name": "base64 non-ascii", "kind": "base64", "value": "aé=="},
    {"name": "base64 invalid character", "kind": "base64", "value": "ab$d"},
    {"name": "base64 url alphabet", "kind": "base64", "value": "ab-_"},
    {"name": "base64 missing padding", "kind": "base64", "value": "abc"},
    {"name": "base64 data after padding", "kind": "base64", "value": "ab==cd=="},
    {"name": "base64 zero bytes", "kind": "base64", "value": "===="},
]

MODELS_REQUESTS = [
    {"name": "models headers", "client_headers": {"X-Team": "a", "Content-Type": "text/plain"}, "headers": {"Accept": "text/html"}},
]

# Retry cases: replies are served in order (repeating the last). Backoff is
# zero and no Retry-After header is sent, so no case sleeps.
RETRIES = [
    {"name": "default 500 exhausts", "replies": [500], "max_retries": 2},
    {"name": "503 then ok", "replies": [503, 200], "max_retries": 2},
    {"name": "400 not retried", "replies": [400], "max_retries": 2},
    {"name": "408 retried", "replies": [408], "max_retries": 1},
    {"name": "429 retried", "replies": [429], "max_retries": 1},
    {"name": "599 retried", "replies": [599], "max_retries": 1},
    {"name": "302 not retried", "replies": [302], "max_retries": 2},
    {"name": "409 not retried", "replies": [409], "max_retries": 2},
    {"name": "422 not retried", "replies": [422], "max_retries": 2},
    {"name": "custom statuses retry 400", "replies": [400], "max_retries": 1, "http_statuses": [400]},
    {"name": "custom statuses skip 500", "replies": [500], "max_retries": 2, "http_statuses": [400]},
    {"name": "empty statuses", "replies": [503], "max_retries": 2, "http_statuses": []},
    {"name": "max retries 4", "replies": [500], "max_retries": 4},
    {"name": "max retries 0", "replies": [500], "max_retries": 0},
    {"name": "user retry header stripped", "replies": [500], "max_retries": 1, "headers": {"X-TypeSafe-Retry-Count": "99"}},
    {"name": "invalid 200 retried by status", "replies": ["invalid"], "max_retries": 2, "http_statuses": [200]},
    {"name": "invalid 200 not retried", "replies": ["invalid"], "max_retries": 2},
    {"name": "last error returned", "replies": [500, 503, 502], "max_retries": 2},
]

RETRY_AFTER = [
    {"retry-after": "2"}, {"retry-after": "0.25"}, {"retry-after": " 2 "}, {"retry-after": "+3"},
    {"retry-after": "bad"}, {"retry-after": "-1"}, {"retry-after": "-0"}, {"retry-after": ""},
    {"retry-after": "1e308"}, {"retry-after": "1e400"}, {"retry-after": "9223372036.9"},
    {"retry-after": "inf"}, {"retry-after": "infinity"}, {"retry-after": "nan"}, {"retry-after": "1_000"}, {"retry-after": "0x10"}, {"retry-after": "0x1p4"}, {"retry-after-ms": "0x1p4", "retry-after": "3"}, {"retry-after": "1e3"},
    {"retry-after": "Wed, 21 Oct 2015 07:28:00 GMT"}, {"retry-after": "Wednesday, 21-Oct-15 07:28:00 GMT"},
    {"retry-after": "Sun Nov  6 08:49:37 1994"},
    {"retry-after-ms": "150"}, {"retry-after-ms": "0.5"}, {"retry-after-ms": "1e-400"},
    {"retry-after-ms": "150", "retry-after": "3"}, {"retry-after-ms": "inf", "retry-after": "3"},
    {"retry-after-ms": "NaN", "retry-after": "3"}, {"retry-after-ms": "-1", "retry-after": "3"},
    {"retry-after-ms": "bad", "retry-after": "3"}, {"retry-after-ms": "-1"}, {"retry-after-ms": ""},
]

API_KEYS = [" sk-padded ", "sk-valid~!@#$%^&*()", "", "   ", "sk a", "sk\ta", "ské", "sk\x01", "sk\x7f"]

# ---------------------------------------------------------------------------


class Capture(logging.Handler):
    def __init__(self) -> None:
        super().__init__(logging.WARNING)
        self.records: list[logging.LogRecord] = []

    def emit(self, record: logging.LogRecord) -> None:
        self.records.append(record)


CAPTURE = Capture()
logging.getLogger("typesafe_sdk").addHandler(CAPTURE)


def client(handler, *, retry: RetryPolicy | None = None, **kwargs) -> TypeSafeClient:
    # Go has no zstd encoder (see the "zstd-compression" note); disabling compression keeps request bodies comparable and
    # the fixture independent of whether this Python has zstd.
    return TypeSafeClient(
        api_key=API_KEY,
        base_url=BASE_URL,
        transport=httpx2.MockTransport(handler),
        retry=retry or RetryPolicy(max_retries=0),
        compression=False,
        **kwargs,
    )


def describe_error(error: BaseException) -> dict:
    out: dict = {"class": type(error).__name__, "message": str(error)}
    if isinstance(error, typesafe_sdk.TypeSafeAPIError):
        out.update(
            status=error.status,
            request_id=error.request_id,
            endpoint=error.endpoint,
            body=error.body,
        )
        if isinstance(error, typesafe_sdk.TypeSafeAPIResponseValidationError):
            out["field_path"] = error.field_path
        if isinstance(error, typesafe_sdk.TypeSafeRateLimitError):
            out["retry_after_ms"] = error.retry_after_ms
    return out


def request_id(resp) -> str | None:
    try:
        return resp.request_id
    except TypeSafeError:
        return None


def describe_system_one(resp) -> dict:
    return {
        "model": resp.model,
        "usage": {"input_tokens": resp.usage.input_tokens, "output_tokens": resp.usage.output_tokens},
        "answers": {name: answer.model_dump(mode="json") for name, answer in resp.answers.items()},
        "groups": {"nouls": sorted(resp.nouls), "choices": sorted(resp.choices), "scores": sorted(resp.scores), "boxes": sorted(resp.boxes)},
        "request_id": request_id(resp),
    }


def run_response(endpoint: str, status: int, headers: dict, body: bytes) -> dict:
    CAPTURE.records.clear()
    c = client(lambda req: httpx2.Response(status, headers=headers, content=body))
    try:
        if endpoint == "models":
            resp = c.models.list()
            result = {"models": [m.model_dump(mode="json") for m in resp.models], "request_id": request_id(resp)}
        else:
            result = describe_system_one(c.system_one("x", PROBE))
        out = {"ok": True, "result": result}
    except typesafe_sdk.TypeSafeError as error:
        out = {"ok": False, "error": describe_error(error)}
    out["unknown_answers"] = [list(r.args) for r in CAPTURE.records]
    return out


def question(spec: dict):
    if "raw" in spec:
        return spec["raw"]
    fields = {k: v for k, v in spec.items() if k != "kind"}
    return {"noul": Noul, "choice": Choice, "score": Score, "box": Box}[spec["kind"]](**fields)


def describe_request(req: httpx2.Request) -> dict:
    headers = {k.lower(): v for k, v in req.headers.items() if k.lower() not in IGNORED_REQUEST_HEADERS}
    out = {"method": req.method, "path": req.url.path, "headers": headers}
    boundary = re.fullmatch(r"multipart/form-data; boundary=(\S+)", headers.get("content-type", ""))
    if boundary:
        # The boundary is random; the parts are described instead.
        headers["content-type"] = "multipart/form-data; boundary=BOUNDARY"
        out["body"], out["parts"] = describe_multipart(req.content, boundary[1].encode())
    else:
        out["body"] = json.loads(req.content) if req.content else None
    return out


def describe_multipart(content: bytes, boundary: bytes) -> tuple[object, list[dict]]:
    """The JSON of the "request" part and every part's headers, with image parts' content as an IMAGES index."""
    delimiter = b"\r\n--" + boundary
    sections = (b"\r\n" + content).split(delimiter)
    if sections[0] != b"" or sections[-1] != b"--\r\n":
        sys.exit(f"unexpected multipart framing: {content[:80]!r}")
    request, parts = None, []
    for section in sections[1:-1]:
        head, _, data = section.removeprefix(b"\r\n").partition(b"\r\n\r\n")
        part = dict(line.decode().lower().split(": ", 1) for line in head.split(b"\r\n"))
        if part.get("content-disposition") == 'form-data; name="request"':
            request = json.loads(data)
        else:
            part["image"] = IMAGES.index(data)
        parts.append(part)
    return request, parts


def revive(value, images: list):
    """Replace {"$image": i} with images[i] and {"$text": template} with the formatted text."""
    if isinstance(value, dict):
        if set(value) == {"$image"}:
            return images[value["$image"]]
        if set(value) == {"$text"}:
            return value["$text"].format(*images)
        return {k: revive(v, images) for k, v in value.items()}
    if isinstance(value, list):
        return [revive(v, images) for v in value]
    return value


def unreference(result: dict, images: list) -> dict:
    """Replace each image's random reference with <image:index>, wherever it appears."""
    text = json.dumps(result)
    for i, image in enumerate(images):
        text = text.replace(str(image), f"<image:{i}>")
    return json.loads(text)


def run_request(case: dict, endpoint: str, images: list | None = None) -> dict:
    images = images if images is not None else [Image(data) for data in IMAGES]
    sent: list[httpx2.Request] = []

    def handler(req: httpx2.Request) -> httpx2.Response:
        sent.append(req)
        return httpx2.Response(200, content=OK_BODY if endpoint == "systemone" else b'{"models":[]}')

    try:
        c = client(handler, model=case.get("client_model"), headers=case.get("client_headers"))
        if endpoint == "models":
            c.models.list(extra_headers=case.get("headers"))
        else:
            questions = {name: question(revive(spec, images)) for name, spec in case["questions"].items()}
            attach = c.attach_assets([images[i] for i in case["attach"]]) if "attach" in case else contextlib.nullcontext()
            with attach:
                c.system_one(revive(case["state"], images), questions, model=case.get("model"), extra_body=revive(case.get("extra_body"), images),
                             extra_headers=case.get("headers"))
        result = {"ok": True, "request": describe_request(sent[0])}
    except Exception as error:  # noqa: BLE001 - pydantic construction errors are part of the observed behavior.
        result = {"ok": False, "error": {"class": type(error).__name__, "message": str(error).splitlines()[0]}, "requests_sent": len(sent)}
    return unreference(result, images)


def run_image_input(case: dict) -> dict:
    try:
        image = Image(base64.b64decode(case["value"])) if case["kind"] == "bytes" else Image.from_base64(case["value"])
    except TypeSafeError as error:
        return {"ok": False, "error": {"class": type(error).__name__, "message": str(error)}, "requests_sent": 0}
    images = [image]
    return run_request({"state": {"$image": 0}, "questions": {"q": NOUL}}, "systemone", images)


def run_retry(case: dict) -> dict:
    seen: list[str | None] = []

    def handler(req: httpx2.Request) -> httpx2.Response:
        seen.append(req.headers.get("x-typesafe-retry-count"))
        reply = case["replies"][min(len(seen), len(case["replies"])) - 1]
        if reply == "invalid":
            return httpx2.Response(200, content=b'{"model":1}')
        if reply == 200:
            return httpx2.Response(200, content=OK_BODY)
        return httpx2.Response(reply, headers={"x-typesafe-request-id": f"req-{len(seen)}"}, content=json.dumps({"error": f"attempt {len(seen)}"}).encode())

    policy = {"max_retries": case["max_retries"], "backoff_initial": 0, "backoff_max": 0}
    if "http_statuses" in case:
        policy["http_statuses"] = set(case["http_statuses"])
    c = client(handler, retry=RetryPolicy(**policy), headers=case.get("headers"))
    try:
        c.system_one("x", PROBE)
        outcome = {"ok": True}
    except typesafe_sdk.TypeSafeError as error:
        outcome = {"ok": False, "error": describe_error(error)}
    return {"attempts": len(seen), "retry_count_headers": seen, "outcome": outcome}


def run_api_key(key: str) -> dict:
    try:
        TypeSafeClient(api_key=key, transport=httpx2.MockTransport(lambda req: httpx2.Response(200)))
        return {"ok": True}
    except TypeSafeError as error:
        return {"ok": False, "message": str(error)}


def finite(value):
    """Replace NaN and infinities, which JSON cannot represent, with markers."""
    if isinstance(value, float) and not math.isfinite(value):
        return {"$nonfinite": repr(value)}
    if isinstance(value, dict):
        return {k: finite(v) for k, v in value.items()}
    if isinstance(value, list):
        return [finite(v) for v in value]
    return value


def check_pin(sdk_dir: str | None) -> None:
    if typesafe_sdk.__version__ != PINNED_VERSION:
        sys.exit(f"typesafe_sdk {typesafe_sdk.__version__} is installed; expected {PINNED_VERSION}")
    if sdk_dir:
        head = subprocess.run(["git", "-C", sdk_dir, "rev-parse", "HEAD"], capture_output=True, text=True, check=True).stdout.strip()
        if head != PINNED_COMMIT:
            sys.exit(f"{sdk_dir} is at {head}; check out {PINNED_COMMIT} (v{PINNED_VERSION})")


def main() -> None:
    check_pin(sys.argv[1] if len(sys.argv) > 1 else None)
    for name in ("TYPESAFE_API_KEY", "TYPESAFE_BASE_URL", "TYPESAFE_DEFAULT_MODEL", "TYPESAFE_LOG_LEVEL"):
        os.environ.pop(name, None)
    os.environ["TYPESAFE_API_KEY"] = ""

    responses = []
    for name, status, headers, body in RESPONSES:
        responses.append({"name": name, "endpoint": "systemone", "status": status, "headers": headers, "body": body,
                          "python": run_response("systemone", status, headers, body.encode())})
    for name, status, headers, b64 in RESPONSES_B64:
        responses.append({"name": name, "endpoint": "systemone", "status": status, "headers": headers, "body_b64": b64,
                          "python": run_response("systemone", status, headers, base64.b64decode(b64))})
    for name, status, headers, body in MODELS:
        responses.append({"name": name, "endpoint": "models", "status": status, "headers": headers, "body": body,
                          "python": run_response("models", status, headers, body.encode())})

    requests = [{**case, "endpoint": "systemone", "python": run_request(case, "systemone")} for case in REQUESTS + IMAGE_REQUESTS]
    requests += [{**case, "endpoint": "models", "python": run_request(case, "models")} for case in MODELS_REQUESTS]

    fixture = {
        "generator": "testdata/parity/generate.py",
        "python_sdk": {"version": PINNED_VERSION, "commit": PINNED_COMMIT},
        "api_key": API_KEY,
        "base_url": BASE_URL,
        "ignored_request_headers": sorted(IGNORED_REQUEST_HEADERS),
        "responses": responses,
        "requests": requests,
        "images": [base64.b64encode(data).decode() for data in IMAGES],
        "image_inputs": [{**case, "python": run_image_input(case)} for case in IMAGE_INPUTS],
        "retries": [{**case, "python": run_retry(case)} for case in RETRIES],
        "retry_after": [{"name": json.dumps(h, sort_keys=True), "headers": h, "python_ms": parse_retry_after(httpx2.Headers(h))} for h in RETRY_AFTER],
        "api_keys": [{"name": json.dumps(k), "key": k, "python": run_api_key(k)} for k in API_KEYS],
    }
    text = json.dumps(finite(fixture), indent=1, ensure_ascii=False, sort_keys=True, allow_nan=False)
    OUTPUT.write_text(text + "\n", encoding="utf-8")
    print(f"wrote {OUTPUT}")


if __name__ == "__main__":
    main()
