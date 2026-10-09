# The development stack's Azurite: the blob service the example stores its
# bytes in, on 10000. This FROM line is Azurite's one pin.
FROM mcr.microsoft.com/azure-storage/azurite:3.37.0

# The image defines no health check. Its busybox nc shows the blob listener
# accepts connections; the image has no curl.
HEALTHCHECK --start-period=30s --start-interval=1s --interval=5s --timeout=3s --retries=5 \
    CMD ["nc", "-z", "127.0.0.1", "10000"]

# The Azure SDK sends an x-ms-version that Azurite rejects unless the version
# check is off. Data lives in /data, the stack's named volume.
CMD ["azurite-blob", "--blobHost", "0.0.0.0", "--skipApiVersionCheck", "-l", "/data"]
