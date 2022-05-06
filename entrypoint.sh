#! /usr/bin/env bash

args=("$@")
/usr/bin/trufflehog --no-verification ${args[@]}
