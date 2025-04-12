#!/bin/bash
# Load credentials from .env file
export $(grep -v '^#' .env | xargs)

# Run the application
./cato-cef-forwarder
