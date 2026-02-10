#!/bin/bash
# Generate RSA key pair for A2A AgentCard signing

set -e

KEY_ID="${1:-default}"
OUTPUT_DIR="${2:-.}"

echo "Generating RSA key pair for A2A AgentCard signing..."
echo "Key ID: $KEY_ID"
echo "Output directory: $OUTPUT_DIR"

# Create output directory if it doesn't exist
mkdir -p "$OUTPUT_DIR"

PRIVATE_KEY="$OUTPUT_DIR/private-key-${KEY_ID}.pem"
PUBLIC_KEY="$OUTPUT_DIR/public-key-${KEY_ID}.pem"

# Generate private key
echo "Generating private key..."
openssl genrsa -out "$PRIVATE_KEY" 2048

# Extract public key
echo "Extracting public key..."
openssl rsa -in "$PRIVATE_KEY" -pubout -out "$PUBLIC_KEY"

echo ""
echo "✓ Key pair generated successfully!"
echo ""
echo "Private key: $PRIVATE_KEY"
echo "Public key:  $PUBLIC_KEY"
echo ""
echo "⚠️  IMPORTANT: Keep the private key secure and never commit it to version control!"
echo ""
echo "Next steps:"
echo "1. Create a Kubernetes Secret with the public key:"
echo "   kubectl create secret generic a2a-public-keys \\"
echo "     --from-file=${KEY_ID}.pem=$PUBLIC_KEY \\"
echo "     --namespace=kagenti-system"
echo ""
echo "2. Sign your agent cards with the private key:"
echo "   python scripts/sign-agent-card.py agent-card.json $PRIVATE_KEY --key-id $KEY_ID"
echo ""


