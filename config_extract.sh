
 for var in $(env | grep '^CONFIG_'); do
    # Extract key and value
    key=${var%%=*}
    value=${var#*=}

    # Remove CONFIG_ prefix and transform __ to .
    config_key=${key#CONFIG_}
    config_key=${config_key//__/.}

    echo "Setting config: $config_key = $value"
    $APP_NAME config set config "$config_key" "$value" --skip-validate
 done
