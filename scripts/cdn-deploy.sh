#!/bin/bash
# cdn-deploy.sh - Deploy WASM files to CDN with SRI hashes
#
# Usage:
#   ./scripts/cdn-deploy.sh [--dry-run]
#
# Environment variables:
#   CDN_BUCKET     - S3/GCS bucket name (default: spacedatanetwork-cdn)
#   CDN_REGION     - Cloud region (default: us-east-1)
#   CDN_ENDPOINT   - Custom S3-compatible endpoint (optional)
#   AWS_PROFILE    - AWS profile to use (optional)

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
WASM_DIR="$PROJECT_ROOT/sdn-js/wasm"

# Configuration
CDN_BUCKET="${CDN_BUCKET:-spacedatanetwork-cdn}"
CDN_REGION="${CDN_REGION:-us-east-1}"
CDN_PATH="wasm"
DRY_RUN=false

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

log_debug() {
    echo -e "${BLUE}[DEBUG]${NC} $1"
}

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --dry-run)
            DRY_RUN=true
            shift
            ;;
        --bucket)
            CDN_BUCKET="$2"
            shift 2
            ;;
        --help)
            echo "Usage: $0 [--dry-run] [--bucket BUCKET_NAME]"
            echo ""
            echo "Deploy WASM files to CDN with SRI hashes"
            echo ""
            echo "Options:"
            echo "  --dry-run     Show what would be uploaded without uploading"
            echo "  --bucket      S3/GCS bucket name"
            echo ""
            echo "Environment variables:"
            echo "  CDN_BUCKET    S3/GCS bucket name"
            echo "  CDN_REGION    Cloud region"
            echo "  CDN_ENDPOINT  Custom S3-compatible endpoint"
            echo "  AWS_PROFILE   AWS profile to use"
            exit 0
            ;;
        *)
            log_error "Unknown option: $1"
            exit 1
            ;;
    esac
done

# Check for required tools
check_tools() {
    local missing=()

    if ! command -v aws &> /dev/null && ! command -v gsutil &> /dev/null; then
        missing+=("aws or gsutil")
    fi

    if ! command -v openssl &> /dev/null; then
        missing+=("openssl")
    fi

    if [ ${#missing[@]} -gt 0 ]; then
        log_error "Missing required tools: ${missing[*]}"
        exit 1
    fi
}

# Generate SRI hash for a file
generate_sri() {
    local file="$1"
    local hash=$(openssl dgst -sha384 -binary "$file" | openssl base64 -A)
    echo "sha384-$hash"
}

# Upload file to S3
upload_s3() {
    local file="$1"
    local dest="$2"
    local content_type="$3"

    local aws_args=(
        "s3" "cp" "$file" "s3://${CDN_BUCKET}/${dest}"
        "--region" "$CDN_REGION"
        "--content-type" "$content_type"
        "--cache-control" "public, max-age=31536000, immutable"
        "--acl" "public-read"
    )

    if [ -n "$CDN_ENDPOINT" ]; then
        aws_args+=("--endpoint-url" "$CDN_ENDPOINT")
    fi

    if $DRY_RUN; then
        log_debug "Would upload: aws ${aws_args[*]}"
    else
        aws "${aws_args[@]}"
    fi
}

# Upload file to GCS
upload_gcs() {
    local file="$1"
    local dest="$2"
    local content_type="$3"

    if $DRY_RUN; then
        log_debug "Would upload: gsutil cp -h \"Content-Type:$content_type\" \"$file\" \"gs://${CDN_BUCKET}/${dest}\""
    else
        gsutil -h "Content-Type:$content_type" \
               -h "Cache-Control:public, max-age=31536000, immutable" \
               cp "$file" "gs://${CDN_BUCKET}/${dest}"
        gsutil acl ch -u AllUsers:R "gs://${CDN_BUCKET}/${dest}"
    fi
}

# Determine which cloud provider to use
get_cloud_provider() {
    if command -v aws &> /dev/null; then
        echo "aws"
    elif command -v gsutil &> /dev/null; then
        echo "gcs"
    else
        log_error "No cloud CLI found"
        exit 1
    fi
}

# Upload a WASM file with its SRI hash
upload_wasm() {
    local file="$1"
    local filename=$(basename "$file")
    local provider=$(get_cloud_provider)

    log_info "Processing: $filename"

    # Generate SRI hash
    local sri=$(generate_sri "$file")
    local sri_file="${file}.sri"

    if $DRY_RUN; then
        log_debug "Would write SRI: $sri > $sri_file"
    else
        echo "$sri" > "$sri_file"
    fi

    log_info "  SRI: $sri"

    # Upload WASM file
    case $provider in
        aws)
            upload_s3 "$file" "${CDN_PATH}/${filename}" "application/wasm"
            upload_s3 "$sri_file" "${CDN_PATH}/${filename}.sri" "text/plain"
            ;;
        gcs)
            upload_gcs "$file" "${CDN_PATH}/${filename}" "application/wasm"
            upload_gcs "$sri_file" "${CDN_PATH}/${filename}.sri" "text/plain"
            ;;
    esac

    log_info "  Uploaded to: https://digitalarsenal.github.io/space-data-network/cdn/${CDN_PATH}/${filename}"
}

# Generate manifest JSON
generate_manifest() {
    local manifest_file="$WASM_DIR/manifest.json"
    local timestamp=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

    log_info "Generating manifest..."

    local manifest="{"
    manifest+="\"version\":\"1.0\","
    manifest+="\"generated\":\"$timestamp\","
    manifest+="\"files\":{"

    local first=true
    for file in "$WASM_DIR"/*.wasm; do
        if [ -f "$file" ]; then
            local filename=$(basename "$file")
            local sri=$(generate_sri "$file")
            local size=$(wc -c < "$file" | tr -d ' ')

            if ! $first; then
                manifest+=","
            fi
            first=false

            manifest+="\"$filename\":{\"sri\":\"$sri\",\"size\":$size}"
        fi
    done

    manifest+="}}"

    if $DRY_RUN; then
        log_debug "Would write manifest: $manifest"
    else
        echo "$manifest" > "$manifest_file"
        log_info "  Written to: $manifest_file"
    fi

    # Upload manifest
    local provider=$(get_cloud_provider)
    case $provider in
        aws)
            upload_s3 "$manifest_file" "${CDN_PATH}/manifest.json" "application/json"
            ;;
        gcs)
            upload_gcs "$manifest_file" "${CDN_PATH}/manifest.json" "application/json"
            ;;
    esac
}

# Invalidate CDN cache (CloudFront)
invalidate_cache() {
    if [ -n "$CLOUDFRONT_DISTRIBUTION_ID" ]; then
        log_info "Invalidating CloudFront cache..."

        if $DRY_RUN; then
            log_debug "Would invalidate: /${CDN_PATH}/*"
        else
            aws cloudfront create-invalidation \
                --distribution-id "$CLOUDFRONT_DISTRIBUTION_ID" \
                --paths "/${CDN_PATH}/*"
        fi
    fi
}

# Main function
main() {
    log_info "Space Data Network CDN Deploy"
    log_info "=============================="

    if $DRY_RUN; then
        log_warn "DRY RUN MODE - No files will be uploaded"
    fi

    check_tools

    log_info "Bucket: $CDN_BUCKET"
    log_info "Region: $CDN_REGION"
    log_info "Path: $CDN_PATH"

    # Check for WASM files
    if ! ls "$WASM_DIR"/*.wasm &> /dev/null; then
        log_warn "No WASM files found in $WASM_DIR"
        log_info "Build WASM files first:"
        log_info "  npx ts-node scripts/build-edge-registry.ts /path/to/relays.json"
        exit 0
    fi

    # Upload each WASM file
    for file in "$WASM_DIR"/*.wasm; do
        if [ -f "$file" ]; then
            upload_wasm "$file"
        fi
    done

    # Generate and upload manifest
    generate_manifest

    # Invalidate cache if configured
    invalidate_cache

    log_info ""
    log_info "Deploy complete!"

    if $DRY_RUN; then
        log_warn "This was a dry run. Use without --dry-run to upload files."
    fi
}

main
