# The development stack's PostgreSQL: the database the integration tier and
# the example connect to through BLOBFS_DSN. This FROM line is PostgreSQL's
# one pin.
FROM postgres:18.6-alpine

# The role, password, and database BLOBFS_DSN names; development values, not
# secrets.
ENV POSTGRES_USER=app \
    POSTGRES_PASSWORD=app \
    POSTGRES_DB=app

# Healthy once the server accepts connections to the app database.
HEALTHCHECK --start-period=30s --start-interval=1s --interval=5s --timeout=3s --retries=5 \
    CMD ["pg_isready", "-U", "app", "-d", "app"]
