# See docs/development.md
FROM mcr.microsoft.com/azure-storage/azurite:3.38.0

HEALTHCHECK --start-period=30s --start-interval=1s --interval=5s --timeout=3s --retries=5 \
    CMD ["nc", "-z", "127.0.0.1", "10000"]

CMD ["azurite-blob", "--blobHost", "0.0.0.0", "--skipApiVersionCheck", "-l", "/data"]
