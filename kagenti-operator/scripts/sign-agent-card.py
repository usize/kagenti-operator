#!/usr/bin/env python3
"""
Sign an A2A AgentCard using JWS Compact Serialization.

Produces signatures conforming to A2A spec section 8.4.2:
  - Protected Header: {"alg": "RS256", "kid": "<key-id>", "spiffe_id": "..."}
  - Payload: canonical JSON of the card (sorted keys, no whitespace, excluding "signatures")
  - Signature: BASE64URL(RSA-SHA256(signingInput))

Usage:
    python sign-agent-card.py <agent-card.json> <private-key.pem> --key-id KEY_ID [--spiffe-id SPIFFE_ID]

Example:
    python sign-agent-card.py weather-agent-card.json private-key.pem --key-id my-key
    python sign-agent-card.py weather-agent-card.json private-key.pem --key-id my-key --spiffe-id spiffe://cluster.local/ns/default/sa/weather
"""

import sys
import json
import base64
import argparse

try:
    from cryptography.hazmat.primitives import hashes, serialization
    from cryptography.hazmat.primitives.asymmetric import padding, rsa, ec
    from cryptography.hazmat.backends import default_backend
except ImportError:
    print("Error: cryptography library not found.")
    print("Install it with: pip install cryptography")
    sys.exit(1)


def load_private_key(key_path):
    """Load a private key from a PEM file."""
    try:
        with open(key_path, 'rb') as f:
            private_key = serialization.load_pem_private_key(
                f.read(),
                password=None,
                backend=default_backend()
            )
        return private_key
    except Exception as e:
        print(f"Error loading private key: {e}")
        sys.exit(1)


def base64url_encode(data: bytes) -> str:
    """Encode bytes to base64url without padding (per RFC 7515)."""
    return base64.urlsafe_b64encode(data).rstrip(b'=').decode('ascii')


def create_canonical_json(card_data):
    """
    Create canonical JSON payload for JWS signing.

    Per A2A spec, the payload is the card's JSON with:
      - "signatures" field excluded
      - Keys sorted alphabetically
      - No whitespace (compact separators)
    """
    card_copy = dict(card_data)
    card_copy.pop('signatures', None)
    card_copy.pop('signature', None)  # Legacy field, just in case

    # Remove empty/None values to match Go canonical JSON behavior
    card_copy = {k: v for k, v in card_copy.items() if v is not None and v != "" and v != [] and v != {}}

    canonical = json.dumps(card_copy, sort_keys=True, separators=(',', ':'))
    return canonical.encode('utf-8')


def build_protected_header(algorithm, key_id, spiffe_id=None):
    """Build the JWS Protected Header as a base64url-encoded string.

    Per A2A spec §8.4.2, the protected header MUST include: alg, typ, kid.
    """
    header = {"alg": algorithm, "kid": key_id, "typ": "JOSE"}
    if spiffe_id:
        header["spiffe_id"] = spiffe_id
    header_json = json.dumps(header, sort_keys=True, separators=(',', ':'))
    return base64url_encode(header_json.encode('utf-8'))


def sign_card_jws(card_data, private_key, key_id, spiffe_id=None):
    """
    Sign an agent card in JWS Compact Serialization format.

    Returns the updated card_data with a "signatures" array containing one entry:
        {"protected": "<base64url>", "signature": "<base64url>"}
    """
    # Determine algorithm from key type
    if isinstance(private_key, rsa.RSAPrivateKey):
        algorithm = 'RS256'
    elif isinstance(private_key, ec.EllipticCurvePrivateKey):
        curve_name = private_key.curve.name
        if curve_name == 'secp256r1':
            algorithm = 'ES256'
        elif curve_name == 'secp384r1':
            algorithm = 'ES384'
        elif curve_name == 'secp521r1':
            algorithm = 'ES512'
        else:
            print(f"Error: Unsupported EC curve: {curve_name}")
            sys.exit(1)
    else:
        print("Error: Unsupported key type. Only RSA and ECDSA keys are supported.")
        sys.exit(1)

    # Build protected header (base64url)
    protected_b64 = build_protected_header(algorithm, key_id, spiffe_id)

    # Build canonical payload (base64url)
    canonical_payload = create_canonical_json(card_data)
    payload_b64 = base64url_encode(canonical_payload)

    # Construct signing input: BASE64URL(header) || '.' || BASE64URL(payload)
    signing_input = f"{protected_b64}.{payload_b64}".encode('ascii')

    # Sign
    if algorithm == 'RS256':
        signature_bytes = private_key.sign(
            signing_input,
            padding.PKCS1v15(),
            hashes.SHA256()
        )
    elif algorithm.startswith('ES'):
        hash_algo = {
            'ES256': hashes.SHA256(),
            'ES384': hashes.SHA384(),
            'ES512': hashes.SHA512(),
        }[algorithm]
        signature_bytes = private_key.sign(
            signing_input,
            ec.ECDSA(hash_algo)
        )
    else:
        print(f"Error: Unsupported algorithm: {algorithm}")
        sys.exit(1)

    signature_b64 = base64url_encode(signature_bytes)

    # Build signatures array (A2A spec allows multiple signatures)
    sig_entry = {
        "protected": protected_b64,
        "signature": signature_b64,
    }

    # Add to card (replace any existing signatures)
    card_data["signatures"] = [sig_entry]

    return card_data, algorithm


def main():
    parser = argparse.ArgumentParser(
        description='Sign an A2A AgentCard using JWS Compact Serialization (A2A spec section 8.4.2)'
    )
    parser.add_argument(
        'card_file',
        help='Path to the agent card JSON file'
    )
    parser.add_argument(
        'key_file',
        help='Path to the private key PEM file'
    )
    parser.add_argument(
        '--key-id',
        required=True,
        help='Key ID to include in the JWS protected header (kid)'
    )
    parser.add_argument(
        '--spiffe-id',
        help='SPIFFE ID to include in the JWS protected header (spiffe_id)'
    )
    parser.add_argument(
        '--output',
        help='Output file (default: overwrite input file)'
    )

    args = parser.parse_args()

    # Load agent card
    try:
        with open(args.card_file, 'r') as f:
            card_data = json.load(f)
    except Exception as e:
        print(f"Error loading agent card: {e}")
        sys.exit(1)

    # Load private key
    private_key = load_private_key(args.key_file)

    # Sign the card in JWS format
    signed_card, algorithm = sign_card_jws(card_data, private_key, args.key_id, args.spiffe_id)

    # Write output
    output_file = args.output or args.card_file
    try:
        with open(output_file, 'w') as f:
            json.dump(signed_card, f, indent=2)
        print(f"Successfully signed agent card (JWS format) → {output_file}")
        print(f"  Algorithm: {algorithm}")
        print(f"  Key ID:    {args.key_id}")
        if args.spiffe_id:
            print(f"  SPIFFE ID: {args.spiffe_id}")
        print(f"  Signatures: {len(signed_card['signatures'])} entry")
    except Exception as e:
        print(f"Error writing signed card: {e}")
        sys.exit(1)


if __name__ == '__main__':
    main()
