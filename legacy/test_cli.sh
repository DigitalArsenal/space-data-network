#!/bin/bash

# Define the path to your application
app_path="./tmp/spacedatanetwork"

# Helper function to run a command and display its output
run_test() {
    echo "Testing command: $1"
    result=$($1)
    echo "$result"
    echo "-----------------------------------"
    echo "$result"
}

# Helper function to check if the output contains the expected text
check_output() {
    if [[ $1 == *$2* ]]; then
        echo "Check successful: found '$2'"
    else
        echo "Check failed: '$2' not found in output"
    fi
}

# Helper function to check if the output does not contain the expected text
check_output_absence() {
    if [[ $1 != *$2* ]]; then
        echo "Check succeeded: '$2'"
    else
        echo "Check failed: Unexpectedly found '$2' in output"
    fi
}


# Set up the mnemonic and hex values
mnemonic="abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
hex_value="0x00000000000000000000000000000000"

# Temporary files for storing keys
mnemonic_file="temp_mnemonic.txt"
hex_file="temp_hex.txt"

# Write the mnemonic and hex values to temporary files
echo $mnemonic >$mnemonic_file
echo $hex_value >$hex_file

# Test --help flag
run_test "$app_path --help"

# Test --env-docs flag
run_test "$app_path --env-docs"

# Test --version flag
run_test "$app_path --version"

# Test --create-server-epm flag
#run_test "$app_path --create-server-epm"

# Test --output-server-epm flag
run_test "$app_path --output-server-epm"

# Test --output-server-epm with QR code output
run_test "$app_path --output-server-epm --qr"

# Test import private key (mnemonic)
run_test "$app_path --import-private-key-mnemonic $mnemonic_file"

# Test import private key (hex)
run_test "$app_path --import-private-key-hex $hex_file"

# Test export private key (mnemonic)
output_mnemonic_file="mnemonic_output.txt"
run_test "$app_path --export-private-key-mnemonic $output_mnemonic_file"
if [ -f "$output_mnemonic_file" ]; then
    echo "Mnemonic file created successfully."
else
    echo "Failed to create mnemonic file."
fi

# Test export private key (hex)
output_hex_file="hex_output.txt"
run_test "$app_path --export-private-key-hex $output_hex_file"
if [ -f "$output_hex_file" ]; then
    echo "Hex file created successfully."
else
    echo "Failed to create hex file."
fi

# Test --add-peerid and --add-fileids
peer_id="16Uiu2HAmP2BnahjRQwuL3JbFDA1Wu14pfF4HsUSsCVNTvYdkEyd9"
file_ids_add="OMM,CAT"
run_test "$app_path --add-peerid $peer_id --add-fileids $file_ids_add"

# Output the current peer/file ID mappings
output=$(run_test "$app_path --list-peers")
check_output "$output" "$peer_id"
check_output "$output" "OMM"
check_output "$output" "CAT"

# Test --remove-peerid and --remove-fileids
file_ids_remove="OMM,CAT"
run_test "$app_path --remove-peerid $peer_id --remove-fileids $file_ids_remove"

# Output the current peer/file ID mappings again to check if removal was successful
output=$(run_test "$app_path --list-peers")
check_output_absence "$output" "$peer_id" # This check should fail if removal was successful
check_output_absence "$output" "OMM"      # This check should fail if removal was successful
check_output_absence "$output" "CAT"      # This check should fail if removal was successful

# Cleanup generated files
echo "Cleaning up generated files..."
rm -f $mnemonic_file $hex_file $output_mnemonic_file $output_hex_file

echo "All tests completed."
