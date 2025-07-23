# Use Eclipse Temurin JRE 19 as the base image (using 19 instead of 21 for better compatibility)
FROM eclipse-temurin:21-jre

# Install necessary dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    bash \
    tzdata \
    ca-certificates \
    fontconfig \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Copy the pre-built JAR file
COPY mock_server/build/libs/mock_server-all.jar /app/mock_server.jar

# Expose the port the server runs on
EXPOSE 8080

# Command to run the application with optimized JVM options for containers
CMD ["java", "-XX:+UseContainerSupport", "-XX:MaxRAMPercentage=75.0", "-jar", "/app/mock_server.jar"]
