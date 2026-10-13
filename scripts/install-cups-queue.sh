#!/bin/bash
set -e

DEFAULT_QUEUE="uniflow_secure"
DEFAULT_DISPLAY="uniFLOW Secure Print"
DEFAULT_SERVER="ipp://127.0.0.1:8631/ipp"
PPD_PATH="/usr/share/cups/model/uniflow-ipp.ppd"

QUEUE_NAME="${1:-$DEFAULT_QUEUE}"
QUEUE_DISPLAY="${2:-$DEFAULT_DISPLAY}"
SERVER_URI="${3:-$DEFAULT_SERVER}"

if [ "$(id -u)" != "0" ]; then
    echo "This script must be run as root."
    exit 1
fi

if ! systemctl is-active --quiet cups 2>/dev/null; then
    echo "Starting CUPS service..."
    systemctl start cups
fi

echo "Installing PPD file..."
mkdir -p /usr/share/cups/model
cp /usr/share/uniflow-ipp/ppd/uniflow-ipp.ppd "$PPD_PATH"

echo "Creating CUPS queue '$QUEUE_NAME'..."
lpadmin -p "$QUEUE_NAME" \
    -E \
    -v "$SERVER_URI" \
    -D "$QUEUE_DISPLAY" \
    -i "$PPD_PATH" \
    -o printer-is-shared=false

cupsenable "$QUEUE_NAME"
cupsaccept "$QUEUE_NAME"

echo "CUPS queue '$QUEUE_NAME' created successfully."
echo "  Queue name:    $QUEUE_NAME"
echo "  Display name:  $QUEUE_DISPLAY"
echo "  Device URI:    $SERVER_URI"
echo "  PPD:           $PPD_PATH"
echo ""
echo "You can now print to this queue using:"
echo "  lp -d $QUEUE_NAME document.pdf"