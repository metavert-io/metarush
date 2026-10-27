"""c2patool's external signer for the content-credentials ability — NOT YET RUNNABLE.

c2patool runs `<signer> --signer-info` and expects {"alg", "sign_cert", "reserve_size"} as
JSON, then pipes the bytes to sign on stdin and reads the raw signature on stdout. The
proposed shape: MetaRush's proxy for the run answers at signer.metarush.invalid with
the certificate chain and signs with the key it keeps in this computer's keychain, so the
key never enters the sandbox; each signature is recorded with the run.
"""
import json
import sys
import urllib.request

SIGNER = "https://signer.metarush.invalid"


def main():
    if "--signer-info" in sys.argv[1:]:
        with urllib.request.urlopen(SIGNER + "/info", timeout=30) as r:
            print(json.dumps(json.load(r)))
        return
    data = sys.stdin.buffer.read()
    req = urllib.request.Request(SIGNER + "/sign", data=data, headers={"Content-Type": "application/octet-stream"})
    with urllib.request.urlopen(req, timeout=30) as r:
        sys.stdout.buffer.write(r.read())


if __name__ == "__main__":
    main()
